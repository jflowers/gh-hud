package io.github.jflowers.ghhud.core

import com.google.protobuf.MessageLite
import io.github.jflowers.ghhud.api.v1.ApproveEpicRequest
import io.github.jflowers.ghhud.api.v1.ClearEpicGateRequest
import io.github.jflowers.ghhud.api.v1.MoveQueuedRequest
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.approveEpicResponse
import io.github.jflowers.ghhud.api.v1.clearEpicGateResponse
import io.github.jflowers.ghhud.api.v1.enrollEpicResponse
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
}
