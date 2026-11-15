package io.github.jflowers.ghhud

import android.content.ActivityNotFoundException
import android.content.Intent
import android.os.Bundle
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.viewModels
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.getValue
import androidx.core.net.toUri
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import io.github.jflowers.ghhud.core.AttentionRow
import io.github.jflowers.ghhud.core.HubAddress

/**
 * The app's one screen: what needs you. Launching it with the extras
 * hub_url (and hub_token, for the Mac's loopback hub) sets the hub, which
 * is how `make android-dev-hub` points it at the Mac over USB.
 */
class MainActivity : ComponentActivity() {
    private val model: HubModel by viewModels()

    private val actions = object : AttentionActions {
        override fun open(url: String) {
            try {
                startActivity(Intent(Intent.ACTION_VIEW, url.toUri()))
            } catch (_: ActivityNotFoundException) {
                Toast.makeText(this@MainActivity, "Nothing can open $url", Toast.LENGTH_SHORT).show()
            }
        }

        override fun approve(row: AttentionRow) = model.approve(row)

        override fun reject(row: AttentionRow) = model.reject(row)

        override fun refresh() = model.refresh()

        override fun setAddress(a: HubAddress) = model.setAddress(a)
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        takeAddress(intent)
        setContent {
            MaterialTheme(colorScheme = if (isSystemInDarkTheme()) darkColorScheme() else lightColorScheme()) {
                val ui by model.ui.collectAsStateWithLifecycle()
                AttentionScreen(ui, actions)
            }
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        takeAddress(intent)
    }

    private fun takeAddress(intent: Intent) {
        val url = intent.getStringExtra(EXTRA_URL) ?: return
        model.setAddress(HubAddress(url, intent.getStringExtra(EXTRA_TOKEN).orEmpty()))
    }

    // The subscription lives only while the app is on screen.
    override fun onStart() {
        super.onStart()
        model.start()
    }

    override fun onStop() {
        model.stop()
        super.onStop()
    }

    companion object {
        const val EXTRA_URL = "hub_url"
        const val EXTRA_TOKEN = "hub_token"
    }
}
