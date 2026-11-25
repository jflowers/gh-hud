package io.github.jflowers.ghhud.core

import com.google.protobuf.Duration
import com.google.protobuf.Timestamp
import io.github.jflowers.ghhud.api.v1.AlertType
import io.github.jflowers.ghhud.api.v1.CheckoutHealth
import io.github.jflowers.ghhud.api.v1.DeweyReindexRequest
import io.github.jflowers.ghhud.api.v1.checkoutHealth
import io.github.jflowers.ghhud.api.v1.deweyPass
import io.github.jflowers.ghhud.api.v1.deweyReindexResponse
import io.github.jflowers.ghhud.api.v1.fleetMachine
import io.github.jflowers.ghhud.api.v1.fleetSnapshot
import io.github.jflowers.ghhud.api.v1.reindexResult
import io.github.jflowers.ghhud.api.v1.alert
import io.github.jflowers.ghhud.api.v1.snapshot
import io.github.jflowers.ghhud.api.v1.run as hubRun
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.runBlocking
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import okio.Buffer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant
import java.time.ZoneOffset

/** The Dewey index on the Workspaces tab, a run's screen and the Fleet (#139). */
class DeweyTest {
    private val now = Instant.parse("2026-11-19T12:00:00Z")
    private val utc = ZoneOffset.UTC

    private fun ts(i: Instant): Timestamp = Timestamp.newBuilder().setSeconds(i.epochSecond).build()
    private fun ago(secs: Long) = ts(now.minusSeconds(secs))

    private val format = "index format 0, this Dewey writes 1"

    /** A checkout whose index Dewey 3.9.0 says to rebuild, also missing embeddings. */
    private val old: CheckoutHealth = checkoutHealth {
        machine = "chai"
        workspace = "api"
        index = "reindex needed: $format, no embeddings"
        indexReasons += listOf(format, "no embeddings")
        indexBuilt = ago(3 * 86400)
        indexStatusAt = ago(300)
        indexPages = 40
        indexBlocks = 200
        indexEmbeddings = 0
    }

    @Test
    fun compactStates() {
        val ok = checkoutHealth {
            index = "ok"
            indexBuilt = ago(2 * 86400)
            indexStatusAt = ago(60)
            indexBlocks = 10
            indexEmbeddings = 10
        }
        assertEquals("ok 2d 100%" to Tone.GOOD, DeweyIndex.state(ok, now))
        assertEquals("old format +1 3d 0%" to Tone.WARN, DeweyIndex.state(old, now))
        assertEquals("unindexed" to Tone.WARN, DeweyIndex.state(checkoutHealth { index = "reindex needed: unindexed" }, now))
        assertEquals(
            "no embeddings 1h" to Tone.WARN,
            DeweyIndex.state(checkoutHealth {
                index = "ok; machine problem: no embedding model on chai"
                indexBuilt = ago(3600)
            }, now),
        )
        assertEquals("no dewey" to Tone.WARN, DeweyIndex.state(checkoutHealth { index = "machine problem: no dewey on PATH" }, now))
        assertEquals("check failed" to Tone.BAD, DeweyIndex.state(checkoutHealth { index = "check failed: exit 1" }, now))
        assertEquals("unchecked" to Tone.MUTED, DeweyIndex.state(checkoutHealth { index = "unchecked" }, now))
        assertEquals("reindexing…" to Tone.PENDING, DeweyIndex.state(old.toBuilder().setReindexing(true).build(), now))
        assertEquals("—" to Tone.MUTED, DeweyIndex.state(checkoutHealth { index = "none" }, now))
        assertEquals("—" to Tone.MUTED, DeweyIndex.state(checkoutHealth {}, now))
        // A machine problem reads stronger than a reindex need.
        assertTrue(DeweyIndex.machineProblem(checkoutHealth { index = "ok; machine problem: no embedding model" }))
        assertFalse(DeweyIndex.machineProblem(old))
        assertEquals("no embeddings +1" to Tone.WARN, DeweyIndex.state(checkoutHealth {
            index = "reindex needed: no embeddings, unindexed"
            indexReasons += listOf("no embeddings", "unindexed")
        }, now))
        assertEquals("dewey: reindex", DeweyIndex.short("pages changed since the index was built"))
    }

    @Test
    fun reasonsKeepTheirCommas() {
        assertEquals(listOf(format, "no embeddings"), DeweyIndex.reasons(old))
        // An older hub without the list: split out of the verdict.
        assertEquals(listOf("unindexed", "no embeddings"), DeweyIndex.reasons(checkoutHealth { index = "reindex needed: unindexed, no embeddings" }))
        val p = DeweyIndex.parts("reindex needed: unindexed; machine problem: no model; check failed: boom")
        assertEquals(DeweyIndex.Parts(listOf("unindexed"), "no model", "boom"), p)
    }

    @Test
    fun aCheckoutsScreen() {
        val h = old.toBuilder()
            .setIndexDeweyVersion("3.8.0")
            .setIndexFormat(0)
            .setLastReindex(
                reindexResult {
                    at = ago(7200)
                    duration = Duration.newBuilder().setSeconds(192).build()
                    error = "dewey reindex: exit 1"
                    reasons += format
                },
            ).build()
        assertEquals(
            listOf(
                Fact("index", "reindex needed (still dispatchable)"),
                Fact("built", "Nov 16, 12:00  (3d ago), format 0 by 3.8.0"),
                Fact("contents", "40 pages · 200 blocks · 0 embedded (0%), as of dewey status 5m ago"),
                Fact("last reindex", "failed after 3m12s: dewey reindex: exit 1, Nov 19, 10:00  (2h ago), for $format"),
            ),
            DeweyIndex.facts(h, now, utc),
        )
        val reasons = DeweyIndex.lists(h).single()
        assertEquals("Reindex needed", reasons.title)
        assertEquals(format, reasons.lines[0])
        assertEquals("no embeddings", reasons.lines[2])
        assertTrue(DeweyIndex.reindexFailed(h))
        assertEquals("reindex failed", WorkspaceRow.of(h).notes)

        val pre39 = checkoutHealth {
            index = "ok"
            indexBuilt = ago(60)
            indexStatusAt = ago(60)
        }
        assertEquals(Fact("built", "Nov 19, 11:59  (1m ago), by a Dewey before 3.9.0 (format 0)"), DeweyIndex.facts(pre39, now, utc)[1])
        assertEquals(listOf(Fact("index", "none (not a Dewey vault)")), DeweyIndex.facts(checkoutHealth { index = "none" }, now, utc))
        assertEquals(
            "ok in 45s; now format 1 by 3.9.0",
            DeweyIndex.reindexLine(reindexResult {
                duration = Duration.newBuilder().setSeconds(45).build()
                deweyVersion = "3.9.0"
                indexFormat = 1
            }, now, utc),
        )
        val machine = DeweyIndex.lists(checkoutHealth { index = "ok; machine problem: no embedding model" }).single()
        assertEquals("a reindex here wouldn't help: fix the machine", machine.note)
    }

    @Test
    fun reindexingTakesNoDispatch() {
        val r = WorkspaceRow.of(checkoutHealth {
            workspace = "api"
            defaultBranch = "main"
            branch = "main"
            reindexing = true
        })
        assertEquals("reindexing" to Tone.PENDING, r.state to r.tone)
        assertFalse(r.ready)
    }

    @Test
    fun plainDirIsUpkeepOnly() {
        val r = WorkspaceRow.of(checkoutHealth {
            machine = "lap1"
            workspace = "acme"
            defaultBranch = "main"
            branch = "main"
            upkeepOnly = true
        })
        assertEquals("upkeep only" to Tone.MUTED, r.state to r.tone)
        assertFalse(r.ready)
    }

    @Test
    fun aRunsCheck() {
        assertNull(DeweyIndex.run(hubRun {}))
        assertEquals("ok" to Tone.GOOD, DeweyIndex.run(hubRun { indexHealth = "ok" }))
        val r = AgentRow.of(
            io.github.jflowers.ghhud.api.v1.Session.newBuilder().setId("s").setRunId("r").build(),
            hubRun {
                id = "r"
                indexHealth = "reindex needed: unindexed"
            },
            emptyList(),
            now,
        )
        val l = AgentDetail.lists(r).single()
        assertEquals(FactList("Dewey index", null, listOf("reindex needed: unindexed"), Tone.WARN), l)
    }

    @Test
    fun aRunsFinding() {
        assertNull(RunFinding.text(hubRun {}))
        assertEquals("the supervisor is reading the run" to Tone.PENDING, RunFinding.text(hubRun { findingPending = true }))
        assertEquals("supervisor: nothing to record: its own test" to Tone.MUTED, RunFinding.text(hubRun { findingNote = "supervisor: nothing to record: its own test" }))
        val blocked = hubRun {
            id = "r"
            finding = io.github.jflowers.ghhud.api.v1.finding {
                kind = "blocked_by_base"
                check = "Build"
                by = "agent"
                why = "main fails it too"
            }
        }
        assertEquals("blocked by the base branch: Build fails there (agent): main fails it too" to Tone.WARN, RunFinding.text(blocked))
        assertEquals(
            "fixed by #9 (supervisor)",
            RunFinding.text(hubRun { finding = io.github.jflowers.ghhud.api.v1.finding { kind = "fixed_by"; pr = 9; by = "supervisor" } })?.first,
        )
        val r = AgentRow.of(io.github.jflowers.ghhud.api.v1.Session.newBuilder().setId("s").setRunId("r").build(), blocked, emptyList(), now)
        assertEquals(FactList("Finding", null, listOf("blocked by the base branch: Build fails there (agent): main fails it too"), Tone.WARN), AgentDetail.lists(r).single())
    }

    @Test
    fun theFleet() {
        val rolling = fleetMachine {
            name = "oak"
            state = "reindexing"
            reindexDone = 7
            reindexTotal = 15
            reindexNote = "reindexed 6/15, 1 failed"
            reindexFailures += (1..7).map { "/w/v$it: exit 1" }
        }
        assertEquals("reindexing 7/15", DeweyFleet.stateText(rolling))
        assertEquals("reindexing 7/15", FleetRow.of(rolling, "", emptyList()).stateText)
        assertEquals(Tone.PENDING, FleetList.tone("reindexing"))
        val lines = DeweyFleet.rolloutLines(rolling)
        assertEquals(ToneLine("dewey: reindexed 6/15, 1 failed", Tone.MUTED), lines[0])
        assertEquals(ToneLine("✗ 2 more", Tone.BAD), lines.last())
        assertEquals(7, lines.size)
        assertEquals("", FleetRow.of(rolling, "", emptyList()).pruned)
        val pruned = fleetMachine { name = "oak"; state = "current"; pruneNote = "mise prune failed: ssh: connection reset" }
        assertEquals("mise prune failed: ssh: connection reset", FleetRow.of(pruned, "", emptyList()).pruned)
        assertEquals(Tone.BAD, FleetRow.of(pruned, "", emptyList()).prunedTone)
        assertEquals(Tone.MUTED, FleetList.pruneTone("mise prune: removed 2 versions, freed 1.1 GB"))

        val running = fleetMachine {
            name = "chai"
            deweyPass = deweyPass {
                running = true
                done = 3
                total = 12
                started = ago(300)
                forced = true
            }
        }
        assertEquals(listOf(ToneLine("dewey pass: reindexing 3/12 · forced · started 5m ago", Tone.PENDING)), DeweyFleet.passLines(running, now))
        val done = fleetMachine {
            name = "elm"
            deweyPass = deweyPass {
                done = 12
                total = 12
                note = "reindexed 10/12, 1 skipped, 1 failed"
                started = ago(3600)
                ended = ago(120)
                failed += "/w/a: exit 1"
                skipped += "/w/b: busy"
            }
        }
        assertEquals(
            listOf(
                ToneLine("dewey pass: reindexed 10/12, 1 skipped, 1 failed · 2m ago", Tone.BAD),
                ToneLine("✗ /w/a: exit 1", Tone.BAD),
                ToneLine("– /w/b: busy", Tone.WARN),
            ),
            DeweyFleet.passLines(done, now),
        )
        assertTrue(DeweyFleet.passLines(fleetMachine { name = "x" }, now).isEmpty())

        val f = fleetSnapshot { machines += listOf(running, done) }
        assertTrue(DeweyCommands.running(f))
        assertTrue(DeweyCommands.running(f, "chai"))
        assertFalse(DeweyCommands.running(f, "elm"))
        assertEquals("Reindex every Dewey vault on every machine?", DeweyCommands.ask(null, true))
        assertEquals("Reindex the Dewey vaults that need it on oak?", DeweyCommands.ask("oak", false))
    }

    @Test
    fun alerts() {
        assertEquals("A Dewey reindex failed", Notices.title(AlertType.ALERT_TYPE_REINDEX_FAILED))
        assertEquals("A checkout's upkeep failed", Notices.title(AlertType.ALERT_TYPE_UPKEEP_FAILED))

        // Both open the checkout's screen.
        assertEquals("chai:api", Notices.checkout(alert { type = AlertType.ALERT_TYPE_REINDEX_FAILED; ref = "chai:api" }))
        assertNull(Notices.checkout(alert { type = AlertType.ALERT_TYPE_BLOCKED; ref = "o/r#1" }))
        val s = snapshot {
            workspaces += checkoutHealth { machine = "chai"; workspace = "api"; repo = "acme/api"; dir = "/w/api" }
            workspaces += checkoutHealth { workspace = "api"; repo = "acme/api"; dir = "/h/api" }
        }
        assertEquals("/w/api", WorkspaceList.byRef(s, "chai:api")?.health?.dir)
        assertEquals("/h/api", WorkspaceList.byRef(s, "api/api")?.health?.dir)
        assertNull(WorkspaceList.byRef(s, "oak:api"))
    }

    @Test
    fun passes() = runBlocking {
        val server = MockWebServer()
        server.start()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        try {
            val hub = HubConnection(HubAddress(server.url("/").toString()), scope)
            server.enqueue(
                MockResponse.Builder().setHeader("Content-Type", "application/proto").body(
                    Buffer().write(
                        deweyReindexResponse {
                            machines += listOf("chai", "elm")
                            refused += "oak: a pass already runs there"
                        }.toByteArray(),
                    ),
                ).build(),
            )
            assertEquals(
                Answer.Ok("Forced Dewey reindex pass started on chai, elm; not on oak: a pass already runs there"),
                DeweyCommands.start(hub, null, force = true),
            )
            val asked = DeweyReindexRequest.parseFrom(server.takeRequest().body!!.toByteArray())
            assertTrue(asked.all && asked.force && !asked.cancel)

            server.enqueue(
                MockResponse.Builder().code(412).setHeader("Content-Type", "application/json")
                    .body("""{"code":"failed_precondition","message":"dewey_reindex is off"}""").build(),
            )
            assertEquals(Answer.No("Dewey: dewey_reindex is off"), DeweyCommands.start(hub, "oak", force = false))
            assertEquals("oak", DeweyReindexRequest.parseFrom(server.takeRequest().body!!.toByteArray()).machine)

            server.enqueue(
                MockResponse.Builder().setHeader("Content-Type", "application/proto")
                    .body(Buffer().write(deweyReindexResponse { machines += "oak" }.toByteArray())).build(),
            )
            assertEquals(Answer.Ok("Dewey reindex pass cancelled on oak"), DeweyCommands.cancel(hub, "oak"))
            val cancel = DeweyReindexRequest.parseFrom(server.takeRequest().body!!.toByteArray())
            assertTrue(cancel.cancel && !cancel.all)
        } finally {
            scope.cancel()
            server.close()
        }
    }
}
