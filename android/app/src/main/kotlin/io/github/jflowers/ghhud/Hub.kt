package io.github.jflowers.ghhud

import android.app.Application
import android.content.Context
import androidx.core.content.ContextCompat
import androidx.core.content.edit
import io.github.jflowers.ghhud.core.HubAddress
import io.github.jflowers.ghhud.core.HubConnection
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.MainScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

class GhHudApp : Application() {
    val hub: Hub by lazy { Hub(this, MainScope()) }
}

val Context.hub: Hub get() = (applicationContext as GhHudApp).hub

/**
 * The app's one connection to the hub, shared by the screen and the
 * notification service. It runs while either holds it, and the hub address
 * and whether to notify are kept across launches. Called on the main thread.
 */
class Hub(private val context: Context, private val scope: CoroutineScope) {
    private val prefs = context.getSharedPreferences("hub", Context.MODE_PRIVATE)
    private val _connection = MutableStateFlow(HubConnection(saved(), scope))
    val connection: StateFlow<HubConnection> = _connection.asStateFlow()
    private val holders = mutableSetOf<String>()

    private val _notify = MutableStateFlow(prefs.getBoolean("notify", false))

    /** Whether the service keeps the connection up to notify you. */
    val notify: StateFlow<Boolean> = _notify.asStateFlow()

    private fun saved() = HubAddress(prefs.getString("url", "").orEmpty(), prefs.getString("token", "").orEmpty())

    /** Keeps the connection up until who lets go. */
    fun hold(who: String) {
        holders += who
        connection.value.start()
    }

    fun release(who: String) {
        holders -= who
        if (holders.isEmpty()) connection.value.stop()
    }

    /** Saves a new hub address and connects to it. */
    fun setAddress(a: HubAddress) {
        val clean = HubAddress(a.url.trim(), a.token.trim())
        if (clean == connection.value.address) return
        prefs.edit {
            putString("url", clean.url)
            putString("token", clean.token)
        }
        connection.value.stop()
        _connection.value = HubConnection(clean, scope)
        if (holders.isNotEmpty()) connection.value.start()
        if (notify.value) startService()
    }

    /** Turns notifications on (starting the service) or off. */
    fun setNotify(on: Boolean) {
        prefs.edit { putBoolean("notify", on) }
        _notify.value = on
        if (on) startService() else context.stopService(HubService.intent(context))
    }

    /**
     * Starts the service if you want notifications and there's a hub. Only
     * from the foreground or at boot: Android refuses it otherwise.
     */
    fun startService() {
        if (notify.value && connection.value.address.isSet) {
            ContextCompat.startForegroundService(context, HubService.intent(context))
        }
    }
}

