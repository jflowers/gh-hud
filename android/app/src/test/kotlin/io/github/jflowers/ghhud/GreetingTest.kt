package io.github.jflowers.ghhud

import org.junit.Assert.assertEquals
import org.junit.Test

/** Runs on the JVM: no device. */
class GreetingTest {
    @Test
    fun greets() {
        assertEquals("Hello from gh-hud", greeting("gh-hud"))
    }
}
