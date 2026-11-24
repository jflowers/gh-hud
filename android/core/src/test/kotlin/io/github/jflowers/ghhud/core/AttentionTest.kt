package io.github.jflowers.ghhud.core

import com.google.protobuf.timestamp
import io.github.jflowers.ghhud.api.v1.SectionCut
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.case
import io.github.jflowers.ghhud.api.v1.decision
import io.github.jflowers.ghhud.api.v1.epic
import io.github.jflowers.ghhud.api.v1.epicNode
import io.github.jflowers.ghhud.api.v1.epicPlan
import io.github.jflowers.ghhud.api.v1.fetchResult
import io.github.jflowers.ghhud.api.v1.item
import io.github.jflowers.ghhud.api.v1.proposal
import io.github.jflowers.ghhud.api.v1.session
import io.github.jflowers.ghhud.api.v1.snapshot
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import java.time.Instant

class AttentionTest {
    private fun at(secs: Long) = timestamp { seconds = secs }

    private val snap: Snapshot = snapshot {
        result = fetchResult {
            user = "jay"
            items += item {
                url = "https://github.com/o/r/pull/1"
                repo = "o/r"
                num = 1
                title = "older review"
                author = "ann"
                categories = Category.REVIEW_REQUESTED.bit
                updatedAt = at(100)
            }
            items += item {
                url = "https://github.com/o/r/pull/2"
                repo = "o/r"
                num = 2
                title = "newer review"
                categories = Category.REVIEW_REQUESTED.bit or Category.CI.bit
                updatedAt = at(200)
            }
            items += item {
                url = "https://github.com/o/r/pull/3"
                repo = "o/r"
                num = 3
                title = "mine, ready"
                categories = Category.MINE.bit or Category.READY_FOR_MAINTAINER.bit
            }
            items += item {
                url = "https://github.com/o/r/issues/4"
                title = "just an issue"
                categories = Category.ISSUES.bit
            }
        }
        sessions += session {
            id = "s1"
            title = "fixing CI"
            task = "fix-ci"
            ref = "o/r#2"
            blocked = true
            blockedSince = at(50)
        }
        sessions += session { id = "s2"; running = true }
        cases += case {
            repo = "o/r"
            check = "build"
            proposal = proposal {
                id = "p1"
                status = "pending"
                decision = decision {
                    kind = "fix_pr"
                    summary = "Retry the flaky build\nmore detail"
                    fixPr = 9
                }
            }
        }
        cases += case { repo = "o/r"; proposal = proposal { id = "p2"; status = "approved" } }
        epics += epic {
            ref = "o/r#10"
            title = "Big thing"
            state = "planned"
            plan = epicPlan {
                id = "plan1"
                nodes += epicNode { ref = "o/r#11"; state = "done" }
                nodes += epicNode { ref = "o/r#12"; state = "ready" }
            }
        }
        epics += epic { ref = "o/r#20"; state = "planned"; plan = epicPlan { id = "old"; stale = true } }
        epics += epic {
            ref = "o/r#30"
            state = "approved"
            plan = epicPlan {
                id = "plan3"
                nodes += epicNode { ref = "o/r#31"; title = "ship it"; state = "gated"; gate = "after the demo" }
                nodes += epicNode { ref = "o/r#32"; state = "gated"; gate = "cleared"; cleared = true }
            }
        }
    }

    @Test
    fun sectionsInOrderNewestFirst() {
        val a = Attention.of(snap)
        assertEquals(listOf("blocked", "proposals", "epics", "gates", "review", "ready"), a.sections.map { it.id })
        assertEquals(7, a.count)
        val review = a.sections.first { it.id == "review" }.rows
        assertEquals(listOf("newer review", "older review"), review.map { it.title })
        assertEquals("o/r#1 · ann", review[1].detail)
        assertEquals(Instant.ofEpochSecond(200), review[0].updated)
    }

    @Test
    fun approvals() {
        val a = Attention.of(snap)
        val rows = a.sections.flatMap { it.rows }.associateBy { it.id }

        val p = rows.getValue("proposal:p1")
        assertEquals("Retry the flaky build", p.title)
        assertEquals("fix pr · o/r · build", p.detail)
        assertEquals("https://github.com/o/r/issues/9", p.url)
        assertEquals(Approval.Proposal("p1"), p.approval)
        assertEquals(true, p.approval!!.rejectable)

        val e = rows.getValue("epic:o/r#10")
        assertEquals("o/r#10 · 2 pieces, 1 open", e.detail)
        assertEquals(Approval.Epic("o/r#10", "plan1"), e.approval)
        assertNull("a stale plan isn't offered", rows["epic:o/r#20"])

        val g = rows.getValue("gate:o/r#30 o/r#31")
        assertEquals("after the demo", g.title)
        assertEquals(Approval.Gate("o/r#30", "o/r#31"), g.approval)
        assertEquals("Clear", g.approval!!.verb)
        assertEquals(false, g.approval!!.rejectable)

        val b = rows.getValue("session:s1")
        assertEquals("fix-ci · o/r#2 · local", b.detail)
        assertEquals("https://github.com/o/r/issues/2", b.url)
    }

    @Test
    fun nothingNeedsYou() {
        assertEquals(Attention.EMPTY, Attention.of(Snapshot.getDefaultInstance()))
    }

    @Test
    fun summary() {
        val now = Instant.ofEpochSecond(10_000)
        val s = snap.toBuilder().setFetchedAt(at(10_000 - 180)).setFollower("42").build()
        assertEquals("jay · updated 3m ago · read-only: gh-hud pid 42 owns dispatch", HubSummary.line(s, now))
        assertEquals("GitHub: bad token", HubSummary.line(s.toBuilder().setFetchError("bad token").build(), now))
        assertEquals("first refresh…", HubSummary.line(Snapshot.getDefaultInstance(), now))
        val cut = SectionCut.newBuilder().setSection("my-prs").setShown(300).setTotal(340).build()
        val withCut = s.toBuilder().setFollower("").setResult(s.result.toBuilder().addCutShort(cut)).build()
        assertEquals("jay · updated 3m ago · my-prs: 300 of 340 shown", HubSummary.line(withCut, now))
        assertEquals("2d ago", HubSummary.ago(Instant.ofEpochSecond(0), Instant.ofEpochSecond(2 * 86400 + 5)))
    }

    @Test
    fun links() {
        assertEquals("https://github.com/o/r/issues/5", Links.forRef("o/r#5"))
        assertNull(Links.forRef("r#5"))
        assertNull(Links.forRef("o/r#x"))
        assertNull(Links.forRef(""))
    }
}
