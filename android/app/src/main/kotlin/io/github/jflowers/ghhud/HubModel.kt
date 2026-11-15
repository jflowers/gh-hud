package io.github.jflowers.ghhud

import android.app.Application
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
    /** Whether the app stays connected to notify you. */
    val notify: Boolean = false,
)

/**
 * What the screen shows, from the app's shared connection (Hub). The
 * screen holds the connection while it's on screen: start and stop follow
 * the activity.
 */
@OptIn(ExperimentalCoroutinesApi::class)
class HubModel(app: Application) : AndroidViewModel(app) {
    private val hub = app.hub
    private val local = MutableStateFlow(Ui(hub.connection.value.address, hub.connection.value.state.value))

    val ui: StateFlow<Ui> = combine(local, hub.connection.flatMapLatest { it.state }, hub.notify) { l, state, notify ->
        // A decided row stays pending until the hub no longer has it waiting.
        val waiting = state.attention.sections.flatMap { s -> s.rows.map { it.id } }.toSet()
        l.copy(
            address = hub.connection.value.address,
            hub = state,
            pending = l.pending intersect waiting,
            notify = notify,
        )
    }.stateIn(viewModelScope, SharingStarted.Eagerly, local.value)

    fun start() {
        hub.hold(HOLDER)
        hub.startService() // in case Android stopped it
    }

    fun stop() = hub.release(HOLDER)

    fun setAddress(a: HubAddress) {
        val before = hub.connection.value
        hub.setAddress(a)
        if (hub.connection.value !== before) local.value = Ui(hub.connection.value.address, hub.connection.value.state.value)
    }

    fun setNotify(on: Boolean) = hub.setNotify(on)

    fun refresh() {
        local.update { it.copy(refreshing = true, refreshError = null) }
        viewModelScope.launch {
            val why = hub.connection.value.refresh()
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
            val why = send(hub.connection.value, a)
            if (why != null) {
                local.update { it.copy(pending = it.pending - row.id, errors = it.errors + (row.id to why)) }
            }
        }
    }

    private companion object {
        const val HOLDER = "screen"
    }
}
