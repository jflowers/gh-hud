package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.epic
import io.github.jflowers.ghhud.api.v1.epicNode
import io.github.jflowers.ghhud.api.v1.epicPlan
import org.junit.Assert.assertEquals
import org.junit.Test

class EpicAreaTest {
    @Test
    fun aPieceShowsItsArea() {
        val e = epic {
            ref = "acme/r#50"
            state = "approved"
            plan = epicPlan {
                id = "p1"
                nodes += epicNode {
                    ref = "acme/r#51"
                    state = "ready"
                    note = "after #49"
                    area += listOf("internal/index", "go.mod")
                }
                nodes += epicNode { ref = "acme/r#52"; state = "ready" }
            }
        }
        val open = EpicRow.of(e).open
        assertEquals("after #49  [internal/index, go.mod]", open[0].note)
        assertEquals("", open[1].note)
    }
}
