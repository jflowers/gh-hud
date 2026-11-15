package io.github.jflowers.ghhud.core

import com.google.protobuf.MessageLite
import com.google.protobuf.timestamp
import io.github.jflowers.ghhud.api.v1.ReadPaneRequest
import io.github.jflowers.ghhud.api.v1.TranscriptRequest
import io.github.jflowers.ghhud.api.v1.readPaneResponse
import io.github.jflowers.ghhud.api.v1.transcriptMessage
import io.github.jflowers.ghhud.api.v1.transcriptPart
import io.github.jflowers.ghhud.api.v1.transcriptResponse
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.take
import kotlinx.coroutines.flow.toList
import kotlinx.coroutines.runBlocking
import mockwebserver3.MockResponse
import mockwebserver3.MockWebServer
import okio.Buffer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Before
import org.junit.Test
import java.time.ZoneOffset
import kotlin.time.Duration.Companion.milliseconds

/** Watch and Transcript against a stand-in hub. */
class ViewersTest {
    private val server = MockWebServer()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private lateinit var hub: HubConnection

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

    @Test
    fun watchReadsAgainAndCarriesOnPastAnError() = runBlocking {
        ok(readPaneResponse { text = "one" })
        server.enqueue(
            MockResponse.Builder().code(500).setHeader("Content-Type", "application/json")
                .body("""{"code":"unknown","message":"run finished and its pane is closed"}""").build(),
        )
        ok(readPaneResponse { text = "three" })
        val reads = Viewers.follow(hub, WatchTarget("run-1", "", "fix"), every = 10.milliseconds, lines = 50).take(3).toList()
        assertEquals(listOf("one", null, "three"), reads.map { it.text })
        assertEquals("run finished and its pane is closed", reads[1].error)
        val req = ReadPaneRequest.parseFrom(server.takeRequest().body!!.toByteArray())
        assertEquals(Triple("run-1", "", 50), Triple(req.runId, req.sessionId, req.lines))
    }

    @Test
    fun transcript() = runBlocking {
        ok(
            transcriptResponse {
                omitted = 3
                messages += transcriptMessage {
                    role = "assistant"
                    created = timestamp { seconds = 1_794_767_756 }
                    parts += transcriptPart { text = "Running the tests." }
                    parts += transcriptPart { tool = "bash"; text = "go test ./..."; failed = true }
                }
            },
        )
        val t = (hub.transcript("run-1", 50) as Answer.Ok).value
        assertEquals(50, TranscriptRequest.parseFrom(server.takeRequest().body!!.toByteArray()).last)
        assertEquals("3 earlier messages not shown", Viewers.omitted(t.omitted))
        assertNull(Viewers.omitted(0))
        assertEquals("assistant · Nov 15, 18:35", Viewers.heading(t.messagesList[0], ZoneOffset.UTC))
        assertEquals("bash  go test ./...  (failed)", Viewers.toolLine(t.messagesList[0].partsList[1]))
    }

    @Test
    fun targets() {
        assertEquals("run:run-1", WatchTarget("run-1", "ses", "x").key)
        assertEquals("session:ses", WatchTarget("", "ses", "x").key)
        assertEquals(WatchTarget("", "ses", "t"), WatchTarget.parse("session:ses", "t"))
        assertNull(WatchTarget.parse("bogus"))
    }
}
