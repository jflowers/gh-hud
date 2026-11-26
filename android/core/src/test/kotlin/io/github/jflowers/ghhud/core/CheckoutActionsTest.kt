package io.github.jflowers.ghhud.core

import com.google.protobuf.Timestamp
import io.github.jflowers.ghhud.api.v1.CheckoutActionKind
import io.github.jflowers.ghhud.api.v1.CheckoutActionRequest
import io.github.jflowers.ghhud.api.v1.checkoutActionResponse
import io.github.jflowers.ghhud.api.v1.checkoutHealth
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
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.Instant

class CheckoutActionsTest {
    private val now = Instant.ofEpochSecond(1_000_000)
    private val clean = checkoutHealth {
        machine = "chai"; workspace = "api"; repo = "acme/api"; dir = "/w/api"
        branch = "main"; defaultBranch = "main"; behind = 2; ahead = 0; index = "ok"
    }

    private fun kinds(h: io.github.jflowers.ghhud.api.v1.CheckoutHealth, enabled: Boolean? = null) =
        WorkspaceActions.actions(h).filter { enabled == null || it.enabled == enabled }.map { it.kind.name.removePrefix("CHECKOUT_ACTION_KIND_") }

    @Test
    fun whatACheckoutOffers() {
        assertEquals(listOf("PULL", "LEARNINGS", "SAVE", "DISCARD", "UPKEEP", "REINDEX"), kinds(clean))
        assertEquals(listOf("PULL", "UPKEEP", "REINDEX"), kinds(clean, enabled = true))
        assertEquals("Fast-forward main (2 behind)", WorkspaceActions.actions(clean).first().title)

        val dirty = clean.toBuilder().addDirty(" M a.txt").setLearnings(2).build()
        assertEquals(listOf("PULL", "SAVE", "DISCARD", "UPKEEP", "REINDEX"), kinds(dirty, enabled = true))
        assertTrue(WorkspaceActions.actions(dirty).first { it.kind == CheckoutActionKind.CHECKOUT_ACTION_KIND_DISCARD }.let { it.confirm && it.destructive })

        val busy = dirty.toBuilder().setUpkeeping(true).build()
        assertEquals(emptyList<String>(), kinds(busy, enabled = true))
        assertEquals(listOf("PULL", "UPKEEP"), kinds(clean.toBuilder().setUpkeepOnly(true).build()))
        assertEquals(emptyList<String>(), kinds(clean.toBuilder().setUnmanaged(true).build()))
        assertTrue(WorkspaceActions.confirmation(CheckoutActionKind.CHECKOUT_ACTION_KIND_DISCARD, dirty).contains("1 uncommitted change(s) in api/api"))
    }

    @Test
    fun whatRunsInACheckout() {
        assertEquals("", WorkspaceActions.running(clean.toBuilder().setAction(CheckoutActionKind.CHECKOUT_ACTION_KIND_SAVE).build()))
        val sweep = clean.toBuilder().setUpkeeping(true).build()
        assertEquals("upkeep", WorkspaceActions.running(sweep))
        assertEquals("upkeep", WorkspaceRow.of(sweep, now).state)
        val saving = sweep.toBuilder().setAction(CheckoutActionKind.CHECKOUT_ACTION_KIND_SAVE)
            .setActionStarted(Timestamp.newBuilder().setSeconds(now.epochSecond - 180)).build()
        val row = WorkspaceRow.of(saving, now)
        assertEquals("saving work to a draft PR · 3m", row.state)
        assertEquals(Tone.PENDING, row.tone)
        assertFalse(row.ready)
        assertEquals("gh-hud is saving work to a draft PR, as asked: no run starts here until it ends.", row.explanation)
        val rx = clean.toBuilder().setReindexing(true).build()
        assertEquals("reindexing", WorkspaceActions.running(rx))
        assertEquals("reindexing (asked for)", WorkspaceActions.running(rx.toBuilder().setAction(CheckoutActionKind.CHECKOUT_ACTION_KIND_REINDEX).build()))
    }

    @Test
    fun startingOne() = runBlocking {
        val server = MockWebServer()
        server.start()
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        try {
            val hub = HubConnection(HubAddress(server.url("/").toString()), scope)
            server.enqueue(
                MockResponse.Builder().setHeader("Content-Type", "application/proto")
                    .body(Buffer().write(checkoutActionResponse { started = "fast-forwarding w/api on chai" }.toByteArray())).build(),
            )
            assertEquals(
                Answer.Ok("Fast-forwarding w/api on chai. The outcome shows on the checkout."),
                WorkspaceCommands.start(hub, clean, CheckoutActionKind.CHECKOUT_ACTION_KIND_PULL),
            )
            val asked = CheckoutActionRequest.parseFrom(server.takeRequest().body!!.toByteArray())
            assertEquals("chai", asked.machine)
            assertEquals("/w/api", asked.dir)
            assertEquals(CheckoutActionKind.CHECKOUT_ACTION_KIND_PULL, asked.action)

            server.enqueue(
                MockResponse.Builder().code(412).setHeader("Content-Type", "application/json")
                    .body("""{"code":"failed_precondition","message":"another upkeep is running on the machine (web)"}""").build(),
            )
            assertEquals(
                Answer.No("chai:api: another upkeep is running on the machine (web)"),
                WorkspaceCommands.start(hub, clean, CheckoutActionKind.CHECKOUT_ACTION_KIND_UPKEEP),
            )
        } finally {
            scope.cancel()
            server.close()
        }
    }
}
