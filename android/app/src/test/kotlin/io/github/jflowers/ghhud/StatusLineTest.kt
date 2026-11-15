package io.github.jflowers.ghhud

import io.github.jflowers.ghhud.core.HubAddress
import io.github.jflowers.ghhud.core.HubState
import io.github.jflowers.ghhud.core.HubStatus
import org.junit.Assert.assertEquals
import org.junit.Test

class StatusLineTest {
    private fun line(s: HubStatus) = statusLine(Ui(HubAddress("https://h"), HubState(s)))

    @Test
    fun saysWhereTheHubIs() {
        assertEquals("No hub set", line(HubStatus.NotSet))
        assertEquals("Connecting to https://h…", line(HubStatus.Connecting))
        assertEquals("Lost the hub: refused. Retrying…", line(HubStatus.Lost("refused")))
        assertEquals("first refresh…", line(HubStatus.Connected))
    }
}
