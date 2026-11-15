package io.github.jflowers.ghhud

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import io.github.jflowers.ghhud.core.Marker
import io.github.jflowers.ghhud.core.Tone

/** How a tone reads on screen, as the Mac colors it. */
@Composable
fun Tone.color(): Color {
    val dark = isSystemInDarkTheme()
    return when (this) {
        Tone.GOOD -> if (dark) Color(0xFF81C995) else Color(0xFF1E8E3E)
        Tone.BAD -> MaterialTheme.colorScheme.error
        Tone.WARN -> if (dark) Color(0xFFFDD663) else Color(0xFFB06000)
        Tone.PENDING -> if (dark) Color(0xFF8AB4F8) else Color(0xFF1A73E8)
        Tone.MUTED -> MaterialTheme.colorScheme.onSurfaceVariant
    }
}

/** The ◆'s color: ready green, your move yellow, needs you orange. */
@Composable
fun Marker.color(): Color = when (this) {
    Marker.READY -> Tone.GOOD.color()
    Marker.YOUR_MOVE -> if (isSystemInDarkTheme()) Color(0xFFFDD663) else Color(0xFFC79100)
    Marker.NEEDS_YOU -> if (isSystemInDarkTheme()) Color(0xFFFCAD70) else Color(0xFFE8710A)
}

val Marker.words: String
    get() = when (this) {
        Marker.READY -> "Ready for a maintainer"
        Marker.YOUR_MOVE -> "Your move"
        Marker.NEEDS_YOU -> "An agent's next step waits on you"
    }
