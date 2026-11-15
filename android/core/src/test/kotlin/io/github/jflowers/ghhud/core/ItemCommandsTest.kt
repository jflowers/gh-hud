package io.github.jflowers.ghhud.core

import com.google.protobuf.MessageLite
import io.github.jflowers.ghhud.api.v1.DispatchRequest
import io.github.jflowers.ghhud.api.v1.dispatchResponse
import io.github.jflowers.ghhud.api.v1.enqueueResponse
import io.github.jflowers.ghhud.api.v1.item
import io.github.jflowers.ghhud.api.v1.menuChoice
import io.github.jflowers.ghhud.api.v1.menuChoicesResponse
import io.github.jflowers.ghhud.api.v1.queueChoicesResponse
import io.github.jflowers.ghhud.api.v1.reinspectResponse
import io.github.jflowers.ghhud.api.v1.run as hubRun
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

/** The item commands against a stand-in hub, in the Mac's words. */
class ItemCommandsTest {
    private val server = MockWebServer()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private lateinit var hub: HubConnection

    private val row = ItemRow.of(
        item {
            url = "https://github.com/acme/r/pull/7"
            repo = "acme/r"
            num = 7
        },
        me = "jay",
        run = hubRun { id = "run-1"; task = "fix"; status = "failed"; finishedAt = com.google.protobuf.timestamp { seconds = 1 } },
    )

    @Before
    fun setUp() {
        server.start()
        hub = HubConnection(HubAddress(server.url("/").toString()), scope)
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
    fun startsAnAgent() = runBlocking {
        ok(menuChoicesResponse { choices += menuChoice { task = "fix"; target = "chai:r-2"; cmd = "opencode run fix" } })
        val choices = ItemCommands.choices(hub, row, queue = false)
        assertEquals(Answer.Ok(listOf(AgentChoice("fix", "chai:r-2", "opencode run fix"))), choices)
        assertEquals("/ghhud.v1.HubService/MenuChoices", server.takeRequest().url.encodedPath)

        ok(dispatchResponse { task = "fix"; workspace = "r-2"; machine = "chai" })
        val said = ItemCommands.run(hub, row, AgentChoice("fix", "chai:r-2"), queue = false)
        assertEquals(Answer.Ok("Started fix on acme/r#7 in chai:r-2"), said)
        val req = DispatchRequest.parseFrom(server.takeRequest().body!!.toByteArray())
        assertEquals("fix" to "https://github.com/acme/r/pull/7", req.task to req.url)

        ok(menuChoicesResponse {})
        assertEquals(
            Answer.No("Nothing can start on this item now: no agent task fits it, or no checkout of acme/r is free."),
            ItemCommands.choices(hub, row, queue = false),
        )
        refused("dispatch is off")
        assertEquals(Answer.No("dispatch is off"), ItemCommands.run(hub, row, AgentChoice("fix", "x"), queue = false))
    }

    @Test
    fun queuesOne() = runBlocking {
        ok(queueChoicesResponse { tasks += listOf("fix", "review") })
        assertEquals(
            Answer.Ok(listOf(AgentChoice("fix", "your queue"), AgentChoice("review", "your queue"))),
            ItemCommands.choices(hub, row, queue = true),
        )
        ok(enqueueResponse { position = 3 })
        assertEquals(
            Answer.Ok("Queued review for acme/r#7, number 3 in your queue"),
            ItemCommands.run(hub, row, AgentChoice("review", "your queue"), queue = true),
        )
        refused("not dispatching")
        assertEquals(Answer.No("Can't queue: not dispatching"), ItemCommands.choices(hub, row, queue = true))
    }

    @Test
    fun rechecksTheLastRun() = runBlocking {
        ok(reinspectResponse { run = hubRun { status = "left-work"; summary = "2 files uncommitted" } })
        assertEquals(
            Answer.Ok("Re-checked fix on acme/r#7: left work behind — 2 files uncommitted"),
            ItemCommands.recheck(hub, row),
        )
        refused("no such run")
        assertEquals(Answer.No("Can't re-check fix on acme/r#7: no such run"), ItemCommands.recheck(hub, row))
    }

    @Test
    fun offWithoutAHub() {
        assertEquals("Not connected to the hub", ItemCommands.off(HubState(HubStatus.Connecting)))
        assertEquals("Dispatch is off on the hub", ItemCommands.off(HubState(HubStatus.Connected)))
    }
}
