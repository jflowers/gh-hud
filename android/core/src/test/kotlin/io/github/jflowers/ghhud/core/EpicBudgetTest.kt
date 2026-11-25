package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.epic
import io.github.jflowers.ghhud.api.v1.epicNode
import io.github.jflowers.ghhud.api.v1.epicPlan
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class EpicBudgetTest {
    private val approved = epic {
        ref = "acme/r#50"
        state = "approved"
        plan = epicPlan {
            id = "p1"
            nodes += epicNode { ref = "acme/r#51"; state = "ready" }
        }
        runs = 7
        runBudget = 20
    }

    @Test
    fun budgetLeft() {
        val r = EpicRow.of(approved)
        assertEquals("approved · 0/1 done, 1 ready, 0 in progress · 7/20 runs", r.status)
        assertNull(r.planToApprove)
        assertNull(r.budgetToGrant)
    }

    @Test
    fun budgetSpent() {
        val r = EpicRow.of(approved.toBuilder().setRuns(20).build())
        assertEquals("run budget spent (20/20 runs) · 0/1 done, 1 ready, 0 in progress", r.status)
        assertEquals(Tone.WARN, r.tone)
        assertEquals("p1", r.planToApprove)
        assertEquals(20, r.budgetToGrant)
    }
}
