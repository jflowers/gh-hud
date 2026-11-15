package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.Snapshot
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import java.io.File
import java.time.ZoneOffset

/** What the agent and checkout screens say, on the shared fixture. */
class DetailsTest {
    private val s = Snapshot.parseFrom(
        File(System.getProperty("fixtures") ?: "../../testdata/clients", "dashboard.binpb").readBytes(),
    )
    private val now = s.fetchedAt.instant
    private val agents = AgentList.rows(s, showStale = true, now = now)
    private val utc = ZoneOffset.UTC

    @Test
    fun aDispatch() {
        val r = agents.first { it.id == "run-0" }
        assertEquals("succeeded" to Tone.GOOD, AgentDetail.headline(r))
        assertEquals(
            listOf(
                Fact("run", "fix acme/api#70"),
                Fact("where", "local:  "),
                Fact("started", "Nov 13, 08:00  (2d ago)"),
                Fact("took", "1h15"),
                Fact("session", "ses-70"),
            ),
            AgentDetail.facts(r, now, utc),
        )
        assertEquals("No final message recorded.", AgentDetail.finalMessage(r))
        assertNull(AgentDetail.finalMessage(agents.first { it.id == "run-3" }))
    }

    @Test
    fun aSessionGhHudDidntStart() {
        val r = agents.first { it.id == "ses-blocked" }
        assertEquals("Port the parser" to Tone.WARN, AgentDetail.headline(r))
        assertEquals(Fact("status", "blocked"), AgentDetail.facts(r, now, utc).first())
        assertEquals(Fact("updated", "Nov 15, 11:40  (20m ago)"), AgentDetail.facts(r, now, utc)[3])
    }

    @Test
    fun checkouts() {
        val ws = WorkspaceList.rows(s)
        val api = ws.first { it.place == "api" }
        assertEquals("Ready. A run here switches to main and fast-forwards it.", WorkspaceDetail.sentence(api))
        assertEquals(Fact("branch", "feature   (default main)"), WorkspaceDetail.facts(api)[1])
        assertEquals(listOf("feature: 3 commits", "spike: 2 commits"), WorkspaceDetail.lists(api).single().lines)
        val dirty = ws.first { it.place == "api-2" }
        assertEquals("Uncommitted (2)", WorkspaceDetail.lists(dirty).single().title)
        assertEquals("No upstream remote.", WorkspaceDetail.sentence(ws.first { it.place == "oak:tools" }))
        assertEquals("branch: 1 commit", WorkspaceDetail.localOnly("branch\t1"))
    }

    @Test
    fun sorts() {
        assertEquals(listOf("ses-failed", "ses-blocked"), AgentSort.STATUS.sort(agents).take(2).map { it.id })
        assertEquals(listOf("run-3", "run-0"), AgentSort.TOOK.sort(agents).take(2).map { it.id })
        assertEquals(agents, AgentSort.HUB.sort(agents, reversed = true))
        val ws = WorkspaceList.rows(s)
        assertEquals("chai:widgets-2", WorkspaceSort.SYNC.sort(ws, reversed = true)[2].place)
        assertEquals(listOf("api-2", "api-3"), WorkspaceSort.STATE.sort(ws).take(2).map { it.place })
    }
}
