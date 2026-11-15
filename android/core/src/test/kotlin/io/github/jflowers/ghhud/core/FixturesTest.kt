package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.Snapshot
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/**
 * The shared fixtures (testdata/clients): what the views make of each
 * snapshot must be what the file says, the same text the Mac's checks
 * render. UPDATE_FIXTURES=1 rewrites the files instead.
 */
class FixturesTest {
    private val dir = File(System.getProperty("fixtures") ?: "../../testdata/clients")

    @Test
    fun viewsMatchTheFixtures() {
        val snaps = dir.listFiles { f -> f.name.endsWith(".binpb") }.orEmpty().sortedBy { it.name }
        assertTrue("no fixtures in $dir", snaps.isNotEmpty())
        for (bin in snaps) {
            val got = Views.render(Snapshot.parseFrom(bin.readBytes()))
            val want = File(dir, bin.name.removeSuffix(".binpb") + ".views.txt")
            if (System.getenv("UPDATE_FIXTURES") == "1") {
                want.writeText(got)
            } else {
                assertTrue("${want.name} is missing: run make client-fixtures", want.exists())
                assertEquals(want.name, want.readText(), got)
            }
        }
    }
}
