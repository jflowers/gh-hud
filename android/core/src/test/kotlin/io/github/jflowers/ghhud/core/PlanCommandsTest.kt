package io.github.jflowers.ghhud.core

import com.google.protobuf.MessageLite
import io.github.jflowers.ghhud.api.v1.ApproveEpicRequest
import io.github.jflowers.ghhud.api.v1.ClearEpicGateRequest
import io.github.jflowers.ghhud.api.v1.FinishEpicRequest
import io.github.jflowers.ghhud.api.v1.MoveQueuedRequest
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.approveEpicResponse
import io.github.jflowers.ghhud.api.v1.clearEpicGateResponse
import io.github.jflowers.ghhud.api.v1.enrollEpicResponse
import io.github.jflowers.ghhud.api.v1.epic
import io.github.jflowers.ghhud.api.v1.epicNode
import io.github.jflowers.ghhud.api.v1.epicPlan
import io.github.jflowers.ghhud.api.v1.finishEpicResponse
import com.google.protobuf.timestamp
import io.github.jflowers.ghhud.api.v1.moveQueuedResponse
import io.github.jflowers.ghhud.api.v1.unqueueResponse
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.runBlocking
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import okio.Buffer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Before
import org.junit.Test
import java.io.File

/** The Plan's commands against a stand-in hub, on the shared fixture's rows. */
class PlanCommandsTest {
    private val server = MockWebServer()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private lateinit var cmds: PlanCommands
    private val s = Snapshot.parseFrom(
        File(System.getProperty("fixtures") ?: "../../testdata/clients", "dashboard.binpb").readBytes(),
    )
    private val queued = PlanList.groups(s).first().rows[1] // fix acme/api#70, 2nd in your queue
    private val epics = PlanList.epics(s)

    @Before
    fun setUp() {
        server.start()
        cmds = PlanCommands(HubConnection(HubAddress(server.url("/").toString()), scope))
    }

    @After
    fun tearDown() {
        scope.cancel()
        server.close()
    }

    private fun ok(m: MessageLite) = server.enqueue(
        MockResponse.Builder().setHeader("Content-Type", "application/proto").body(Buffer().write(m.toByteArray())).build(),
    )

    private fun refused(why: String) = server.enqueue(
        MockResponse.Builder().code(412).setHeader("Content-Type", "application/json")
            .body("""{"code":"failed_precondition","message":"$why"}""").build(),
    )

    @Test
    fun yourQueue() = runBlocking {
        ok(moveQueuedResponse { index = 0 })
        assertEquals(Answer.Ok(""), cmds.move(queued, -1))
        val req = MoveQueuedRequest.parseFrom(server.takeRequest().body!!.toByteArray())
        assertEquals(Triple("fix", "acme/api#70", -1L), Triple(req.task, req.ref, req.delta))

        ok(unqueueResponse {})
        assertEquals(Answer.Ok("Took fix acme/api#70 off your queue"), cmds.unqueue(queued))
        refused("not queued")
        assertEquals(Answer.No("Can't unqueue fix acme/api#70: not queued"), cmds.unqueue(queued))
    }

    @Test
    fun epics() = runBlocking {
        val planned = epics.first { it.ref == "acme/api#90" }
        ok(approveEpicResponse {})
        assertEquals(Answer.Ok("Approved the plan for acme/api#90"), cmds.approve(planned))
        assertEquals("plan-90", ApproveEpicRequest.parseFrom(server.takeRequest().body!!.toByteArray()).planId)

        val approved = epics.first { it.ref == "acme/widgets#60" }
        assertEquals(Answer.No("No plan of acme/widgets#60 waits for you"), cmds.approve(approved))
        val gate = approved.open.first { it.clearable }
        ok(clearEpicGateResponse {})
        assertEquals(Answer.Ok("Cleared the gate on acme/widgets#63"), cmds.clearGate(approved.ref, gate.ref))
        val g = ClearEpicGateRequest.parseFrom(server.takeRequest().body!!.toByteArray())
        assertEquals("acme/widgets#60" to "acme/widgets#63", g.ref to g.piece)

        ok(enrollEpicResponse { ref = "acme/docs#8" })
        assertEquals(Answer.Ok("Enrolled acme/docs#8; its plan shows here once it's read"), cmds.enroll("  acme/docs#8 "))
        refused("not an issue")
        assertEquals(Answer.No("not an issue"), cmds.enroll("nope"))
    }

    @Test
    fun finishAnEpic() = runBlocking {
        val finished = epic {
            ref = "acme/docs#8"
            state = "approved"
            plan = epicPlan {
                id = "plan-8"
                nodes += epicNode { ref = "acme/docs#9"; state = "done" }
                nodes += epicNode { ref = "acme/docs#10"; state = "dropped" }
            }
            complete = timestamp { seconds = 1 }
            summary = "**Epic finished.** All 2 pieces…"
        }
        val row = EpicRow.of(finished)
        assertEquals("plan-8" to "**Epic finished.** All 2 pieces…", row.planToFinish to row.summary)
        assertEquals("every piece finished · 1/2 done, 0 ready, 0 in progress" to Tone.WARN, row.status to row.tone)
        ok(finishEpicResponse {})
        assertEquals(Answer.Ok("Posted the summary and closed acme/docs#8"), cmds.finish(row))
        val req = FinishEpicRequest.parseFrom(server.takeRequest().body!!.toByteArray())
        assertEquals("acme/docs#8" to "plan-8", req.ref to req.planId)

        val closed = EpicRow.of(finished.toBuilder().setState("closed").setClosedNote("closed on GitHub").build())
        assertEquals(null, closed.planToFinish)
        assertEquals("closed on GitHub · 1/2 done, 0 ready, 0 in progress", closed.status)
        assertEquals(Answer.No("acme/docs#8 isn't finished"), cmds.finish(closed))
        assertEquals(null, EpicRow.of(finished.toBuilder().clearComplete().build()).planToFinish)
        val held = EpicRow.of(finished.toBuilder().clearComplete().setHeld("hold").build())
        assertEquals("held by the hold label · 1/2 done, 0 ready, 0 in progress" to Tone.WARN, held.status to held.tone)
    }
}
