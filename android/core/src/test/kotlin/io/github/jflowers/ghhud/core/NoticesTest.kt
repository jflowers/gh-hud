package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.AlertType
import io.github.jflowers.ghhud.api.v1.alert
import io.github.jflowers.ghhud.api.v1.run as hubRun
import io.github.jflowers.ghhud.api.v1.session
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class NoticesTest {
    private fun row(id: String, approval: Approval? = Approval.Proposal(id)) =
        AttentionRow(id, "t $id", "d", null, null, approval)

    private fun attention(vararg rows: AttentionRow) =
        Attention(listOf(AttentionSection("s", "S", rows.toList())), rows.size)

    @Test
    fun announcesOnlyNewApprovals() {
        val w = ApprovalWatch()
        // Waiting before we looked: not news.
        assertEquals(ApprovalWatch.Change(emptyList(), emptySet()), w.update(attention(row("a"))))

        val b = row("b")
        val review = row("https://github.com/o/r/pull/1", approval = null)
        assertEquals(ApprovalWatch.Change(listOf(b), emptySet()), w.update(attention(row("a"), b, review)))

        // a was decided; the review was never an approval.
        assertEquals(ApprovalWatch.Change(emptyList(), setOf("a")), w.update(attention(b)))
        assertEquals(ApprovalWatch.Change(emptyList(), setOf("b")), w.update(Attention.EMPTY))
    }

    @Test
    fun alertTitlesAndLinks() {
        assertEquals("An agent is waiting on you", Notices.title(AlertType.ALERT_TYPE_BLOCKED))
        assertEquals("gh-hud", Notices.title(AlertType.ALERT_TYPE_UNSPECIFIED))
        assertEquals("An epic gate only you can clear", Notices.title(Approval.Gate("o/r#1", "o/r#2")))

        assertEquals("https://github.com/o/r/issues/3", Notices.link(alert { ref = "o/r#3" }))
        assertEquals("https://github.com/o/r/issues/4", Notices.link(alert { run = hubRun { ref = "o/r#4" } }))
        assertEquals("https://github.com/o/r/issues/5", Notices.link(alert { session = session { ref = "o/r#5" } }))
        assertNull(Notices.link(alert { text = "no ref" }))
    }
}
