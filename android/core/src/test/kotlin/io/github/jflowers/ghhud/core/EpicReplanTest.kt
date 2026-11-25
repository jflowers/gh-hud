package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.epic
import io.github.jflowers.ghhud.api.v1.epicNode
import io.github.jflowers.ghhud.api.v1.epicPlan
import io.github.jflowers.ghhud.api.v1.epicReplan
import org.junit.Assert.assertEquals
import org.junit.Test

class EpicReplanTest {
    @Test
    fun pendingReplansShow() {
        val e = epic {
            ref = "acme/r#50"
            state = "approved"
            plan = epicPlan {
                id = "p1"
                nodes += epicNode { ref = "acme/r#52"; state = "ready" }
            }
            replans += epicReplan { id = "r1"; kind = "drop"; piece = "acme/r#52"; why = "done in #9"; status = "pending" }
            replans += epicReplan { id = "r2"; kind = "follow_up"; piece = "acme/r#52"; title = "Fix it"; why = "found a bug"; status = "pending" }
            replans += epicReplan { id = "r3"; kind = "drop"; piece = "acme/r#52"; status = "rejected" }
        }
        assertEquals(
            listOf(
                EpicRow.Replan("r1", "drop #52 (close it as not planned)", "done in #9"),
                EpicRow.Replan("r2", "new piece “Fix it”, from #52", "found a bug"),
            ),
            EpicRow.of(e).replans,
        )
    }
}
