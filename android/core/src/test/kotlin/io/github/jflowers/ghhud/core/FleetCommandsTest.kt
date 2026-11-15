package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.FleetAction
import io.github.jflowers.ghhud.api.v1.FleetControlRequest
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.fleetControlResponse
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
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import java.io.File

/** The rollout's controls against a stand-in hub, on the shared fixture's fleet. */
class FleetCommandsTest {
    private val server = MockWebServer()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private lateinit var hub: HubConnection
    private val dir = System.getProperty("fixtures") ?: "../../testdata/clients"
    private val s = Snapshot.parseFrom(File(dir, "dashboard.binpb").readBytes())

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

    @Test
    fun controls() = runBlocking {
        server.enqueue(
            MockResponse.Builder().setHeader("Content-Type", "application/proto")
                .body(Buffer().write(fleetControlResponse {}.toByteArray())).build(),
        )
        assertEquals(Answer.Ok("Retrying oak"), FleetCommands.run(hub, FleetAction.FLEET_ACTION_RETRY, s.fleet))
        assertEquals(
            FleetAction.FLEET_ACTION_RETRY,
            FleetControlRequest.parseFrom(server.takeRequest().body!!.toByteArray()).action,
        )
        server.enqueue(
            MockResponse.Builder().code(412).setHeader("Content-Type", "application/json")
                .body("""{"code":"failed_precondition","message":"nothing failed"}""").build(),
        )
        assertEquals(Answer.No("Fleet: nothing failed"), FleetCommands.run(hub, FleetAction.FLEET_ACTION_PAUSE, s.fleet))
    }

    @Test
    fun whatApplies() {
        assertTrue(FleetCommands.canSkipWait(s.fleet)) // chai drains
        assertTrue(FleetCommands.canRetry(s.fleet)) // oak failed
        val ro = Snapshot.parseFrom(File(dir, "readonly.binpb").readBytes())
        assertFalse(FleetCommands.canRetry(ro.fleet))
        assertEquals("The fleet is off on the hub", FleetCommands.off(HubState(HubStatus.Connected, ro)))
        assertEquals(null, FleetCommands.off(HubState(HubStatus.Connected, s)))
        assertEquals(listOf("oak", "elm", "chai"), FleetSort.STATE.sort(FleetList.rows(s)).take(3).map { it.name })
    }
}
