package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.AlertType
import io.github.jflowers.ghhud.api.v1.ApproveProposalRequest
import io.github.jflowers.ghhud.api.v1.ToolkitAction
import io.github.jflowers.ghhud.api.v1.ToolkitControlRequest
import io.github.jflowers.ghhud.api.v1.ToolkitRolloutRequest
import io.github.jflowers.ghhud.api.v1.EventKind
import io.github.jflowers.ghhud.api.v1.SubscribeResponse
import io.github.jflowers.ghhud.api.v1.alert
import io.github.jflowers.ghhud.api.v1.event
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.fetchResult
import io.github.jflowers.ghhud.api.v1.item
import io.github.jflowers.ghhud.api.v1.snapshot
import io.github.jflowers.ghhud.api.v1.subscribeResponse
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import okio.Buffer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Before
import org.junit.Test
import java.util.concurrent.TimeUnit

/** The connection against a stand-in hub that speaks the Connect protocol. */
class HubConnectionTest {
    private val server = MockWebServer()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    @Before
    fun setUp() = server.start()

    @After
    fun tearDown() {
        scope.cancel()
        server.close()
    }

    private fun reviews(vararg titles: String): Snapshot = snapshot {
        result = fetchResult {
            titles.forEachIndexed { i, t ->
                items += item {
                    url = "https://github.com/o/r/pull/$i"
                    title = t
                    categories = Category.REVIEW_REQUESTED.bit
                }
            }
        }
    }

    /**
     * A Subscribe response: each snapshot as an envelope, then the end.
     * With hold, the end comes only after a while, so the connection
     * stays up long enough to see.
     */
    private fun stream(vararg snaps: Snapshot, hold: Boolean = false): MockResponse =
        messages(*snaps.map { subscribeResponse { snapshot = it } }.toTypedArray(), hold = hold)

    private fun messages(vararg msgs: SubscribeResponse, hold: Boolean = false): MockResponse {
        val body = Buffer()
        for (m in msgs) {
            val msg = m.toByteArray()
            body.writeByte(0).writeInt(msg.size).write(msg)
        }
        val end = "{}".toByteArray()
        val snapBytes = body.size
        body.writeByte(2).writeInt(end.size).write(end)
        val b = MockResponse.Builder()
            .setHeader("Content-Type", "application/connect+proto")
            .body(body)
        if (hold) b.throttleBody(snapBytes, 30, TimeUnit.SECONDS)
        return b.build()
    }

    private fun connect(token: String = "") =
        HubConnection(HubAddress(server.url("/").toString(), token), scope).also { it.start() }

    @Test
    fun showsWhatNeedsYou() = runBlocking {
        server.enqueue(stream(reviews("a", "b"), hold = true))
        val hub = connect(token = "t0k")
        val state = withTimeout(10_000) { hub.state.first { it.status == HubStatus.Connected } }
        assertEquals(2, state.attention.count)
        hub.stop()

        val req = server.takeRequest()
        assertEquals("/ghhud.v1.HubService/Subscribe", req.url.encodedPath)
        assertEquals("Bearer t0k", req.headers["Authorization"])
    }

    @Test
    fun subscriptionHasNoDeadline() = runBlocking {
        // connect-kotlin's default gave every call 10s, the subscription too:
        // the hub dropped it every 10s ("10s timeout elapsed").
        server.enqueue(stream(reviews("a"), hold = true))
        val hub = connect()
        withTimeout(10_000) { hub.state.first { it.status == HubStatus.Connected } }
        assertNull(server.takeRequest().headers["Connect-Timeout-Ms"])

        server.enqueue(MockResponse.Builder().setHeader("Content-Type", "application/proto").build())
        assertNull(hub.approve(Approval.Proposal("p1")))
        assertEquals("30000", server.takeRequest().headers["Connect-Timeout-Ms"])
        hub.stop()
    }

    @Test
    fun reconnectsWhenTheHubGoesAway() = runBlocking {
        server.enqueue(stream(reviews("a")))
        server.enqueue(stream(reviews("a", "b", "c"), hold = true))
        val hub = connect()
        val state = withTimeout(10_000) { hub.state.first { it.attention.count == 3 } }
        assertEquals(HubStatus.Connected, state.status)
        assertEquals(2, server.requestCount)
        hub.stop()
    }

    @Test
    fun passesOnAlerts() = runBlocking {
        server.enqueue(
            messages(
                subscribeResponse { event = event { kind = EventKind.EVENT_KIND_NOTICE; text = "refreshed" } },
                subscribeResponse {
                    event = event {
                        kind = EventKind.EVENT_KIND_ALERT
                        alert = alert { type = AlertType.ALERT_TYPE_BLOCKED; ref = "o/r#2"; text = "fix-ci waits" }
                    }
                },
                hold = true,
            ),
        )
        val hub = HubConnection(HubAddress(server.url("/").toString()), scope)
        val got = scope.async { hub.alerts.first() }
        hub.start()
        val a = withTimeout(10_000) { got.await() }
        assertEquals("fix-ci waits", a.text)
        assertEquals(AlertType.ALERT_TYPE_BLOCKED, a.type)
        hub.stop()
    }

    @Test
    fun saysWhyWhenRefused() = runBlocking {
        // As the hub refuses a stream: the reason in the end of the stream.
        val end = """{"error":{"code":"permission_denied","message":"only the hub's own tailnet user can use it"}}"""
            .toByteArray()
        server.enqueue(
            MockResponse.Builder()
                .setHeader("Content-Type", "application/connect+proto")
                .body(Buffer().writeByte(2).writeInt(end.size).write(end))
                .build(),
        )
        val hub = connect()
        val state = withTimeout(10_000) { hub.state.first { it.status is HubStatus.Lost } }
        assertEquals(HubStatus.Lost("only the hub's own tailnet user can use it"), state.status)
        hub.stop()
    }

    @Test
    fun approvesAndRejects() = runBlocking {
        val hub = HubConnection(HubAddress(server.url("/").toString()), scope) // not subscribed
        server.enqueue(MockResponse.Builder().setHeader("Content-Type", "application/proto").build())
        assertNull(hub.approve(Approval.Proposal("p1")))
        val req = server.takeRequest()
        assertEquals("/ghhud.v1.HubService/ApproveProposal", req.url.encodedPath)
        assertEquals("p1", ApproveProposalRequest.parseFrom(req.body!!.toByteArray()).id)

        server.enqueue(
            MockResponse.Builder().code(412)
                .setHeader("Content-Type", "application/json")
                .body("""{"code":"failed_precondition","message":"plan replaced"}""")
                .build(),
        )
        assertEquals("plan replaced", hub.approve(Approval.Epic("o/r#1", "old")))
        assertEquals("only a proposal or a release can be turned down", hub.reject(Approval.Gate("o/r#1", "o/r#2")))

        server.enqueue(MockResponse.Builder().setHeader("Content-Type", "application/proto").build())
        assertNull(hub.approve(Approval.Release("uf@0.22.0", "uf", "0.22.0")))
        var r = server.takeRequest() // the failed epic approval
        r = server.takeRequest()
        assertEquals("/ghhud.v1.HubService/ToolkitRollout", r.url.encodedPath)
        val roll = ToolkitRolloutRequest.parseFrom(r.body!!.toByteArray())
        assertEquals("uf" to "0.22.0", roll.tool to roll.version)

        server.enqueue(MockResponse.Builder().setHeader("Content-Type", "application/proto").build())
        assertNull(hub.reject(Approval.Release("uf@0.22.0", "uf", "0.22.0")))
        r = server.takeRequest()
        assertEquals("/ghhud.v1.HubService/ToolkitControl", r.url.encodedPath)
        val ctl = ToolkitControlRequest.parseFrom(r.body!!.toByteArray())
        assertEquals(ToolkitAction.TOOLKIT_ACTION_DISMISS to "uf@0.22.0", ctl.action to ctl.id)
    }

    @Test
    fun noAddressNoConnection() {
        val hub = HubConnection(HubAddress(""), scope)
        hub.start()
        assertEquals(HubStatus.NotSet, hub.state.value.status)
        assertEquals(0, server.requestCount)
    }
}
