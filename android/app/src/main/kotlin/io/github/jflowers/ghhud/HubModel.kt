package io.github.jflowers.ghhud

import android.app.Application
import android.content.Context
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import io.github.jflowers.ghhud.core.Approval
import io.github.jflowers.ghhud.core.AttentionRow
import io.github.jflowers.ghhud.core.HubAddress
import io.github.jflowers.ghhud.core.HubConnection
import io.github.jflowers.ghhud.core.HubState
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.flatMapLatest
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/** What the screen shows. */
data class Ui(
    val address: HubAddress,
    val hub: HubState,
    /** Rows whose approval is on its way to the hub. */
    val pending: Set<String> = emptySet(),
    /** Why the last approval of a row failed. */
    val errors: Map<String, String> = emptyMap(),
    val refreshing: Boolean = false,
    /** Why the last refresh failed. */
    val refreshError: String? = null,
)

/**
 * The hub address (kept across launches) and the connection to it. The
 * connection runs while the app is on screen: start and stop follow the
 * activity.
 */
@OptIn(ExperimentalCoroutinesApi::class)
class HubModel(app: Application) : AndroidViewModel(app) {
    private val prefs = app.getSharedPreferences("hub", Context.MODE_PRIVATE)
    private val connection = MutableStateFlow(connect(saved()))
    private val local = MutableStateFlow(Ui(connection.value.address, connection.value.state.value))
    private var started = false

    val ui: StateFlow<Ui> = combine(local, connection.flatMapLatest { it.state }) { l, hub ->
        // A decided row stays pending until the hub no longer has it waiting.
        val waiting = hub.attention.sections.flatMap { s -> s.rows.map { it.id } }.toSet()
        l.copy(address = connection.value.address, hub = hub, pending = l.pending intersect waiting)
    }.stateIn(viewModelScope, SharingStarted.Eagerly, local.value)

    private fun saved() = HubAddress(prefs.getString("url", "").orEmpty(), prefs.getString("token", "").orEmpty())

    private fun connect(a: HubAddress) = HubConnection(a, viewModelScope)

    fun start() {
        started = true
        connection.value.start()
    }

    fun stop() {
        started = false
        connection.value.stop()
    }

    /** Saves a new hub address and connects to it. */
    fun setAddress(a: HubAddress) {
        val clean = HubAddress(a.url.trim(), a.token.trim())
        if (clean == connection.value.address) return
        prefs.edit().putString("url", clean.url).putString("token", clean.token).apply()
        connection.value.stop()
        local.value = Ui(clean, connection.value.state.value)
        connection.value = connect(clean)
        if (started) connection.value.start()
    }

    fun refresh() {
        local.update { it.copy(refreshing = true, refreshError = null) }
        viewModelScope.launch {
            val why = connection.value.refresh()
            local.update { it.copy(refreshing = false, refreshError = why) }
        }
    }

    fun approve(row: AttentionRow) = decide(row) { conn, a -> conn.approve(a) }

    fun reject(row: AttentionRow) = decide(row) { conn, a -> conn.reject(a) }

    /**
     * Sends a decision. The row stays, marked pending, until the hub's
     * next snapshot no longer has it waiting; a refusal shows under it and
     * offers the buttons again.
     */
    private fun decide(row: AttentionRow, send: suspend (HubConnection, Approval) -> String?) {
        val a = row.approval ?: return
        local.update { it.copy(pending = it.pending + row.id, errors = it.errors - row.id) }
        viewModelScope.launch {
            val why = send(connection.value, a)
            if (why != null) {
                local.update { it.copy(pending = it.pending - row.id, errors = it.errors + (row.id to why)) }
            }
        }
    }
}
