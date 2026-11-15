package io.github.jflowers.ghhud

import android.Manifest
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
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

        override fun setNotify(on: Boolean) {
            val asked = Build.VERSION.SDK_INT >= 33 &&
                checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
            if (on && asked) permission.launch(Manifest.permission.POST_NOTIFICATIONS) else model.setNotify(on)
        }
    }

    // Notifying needs the permission (Android 13 and later); without it the
    // switch stays off.
    private val permission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        if (granted) {
            model.setNotify(true)
        } else {
            Toast.makeText(this, "gh-hud can't notify you without the permission", Toast.LENGTH_LONG).show()
        }
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

    // The screen holds the subscription while it's on screen; the service,
    // when you asked to be notified, holds it the rest of the time.
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
