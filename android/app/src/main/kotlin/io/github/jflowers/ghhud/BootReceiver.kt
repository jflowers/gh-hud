package io.github.jflowers.ghhud

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** Starts notifying again after the phone restarts, if you asked for it. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action == Intent.ACTION_BOOT_COMPLETED) context.hub.startService()
    }
}
