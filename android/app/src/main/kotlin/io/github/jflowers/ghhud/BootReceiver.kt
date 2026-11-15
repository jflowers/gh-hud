package io.github.jflowers.ghhud

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/**
 * Starts notifying again, if you asked for it, after the phone restarts
 * and after the app is updated: an update stops the service, and nothing
 * else starts it until you open the app. Android lets both start a
 * foreground service from the background.
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action in STARTS) context.hub.startService()
    }

    companion object {
        val STARTS = setOf(Intent.ACTION_BOOT_COMPLETED, Intent.ACTION_MY_PACKAGE_REPLACED)
    }
}
