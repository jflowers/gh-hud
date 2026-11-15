package io.github.jflowers.ghhud

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.foundation.layout.padding

/** The spike's whole app: one screen that says hello. */
class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        setContent {
            MaterialTheme {
                Scaffold { inner ->
                    Box(Modifier.fillMaxSize().padding(inner), contentAlignment = Alignment.Center) {
                        Greeting("gh-hud")
                    }
                }
            }
        }
    }
}

/** "Hello from gh-hud". */
fun greeting(name: String): String = "Hello from $name"

@Composable
fun Greeting(name: String) {
    Text(greeting(name), style = MaterialTheme.typography.headlineMedium, modifier = Modifier.testTag("greeting"))
}
