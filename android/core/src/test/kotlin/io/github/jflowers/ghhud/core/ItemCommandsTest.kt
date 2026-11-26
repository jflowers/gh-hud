package io.github.jflowers.ghhud.core

import com.google.protobuf.MessageLite
import io.github.jflowers.ghhud.api.v1.DispatchRequest
import io.github.jflowers.ghhud.api.v1.ItemChoicesRequest
import io.github.jflowers.ghhud.api.v1.ItemKind
import io.github.jflowers.ghhud.api.v1.itemChoicesResponse
import io.github.jflowers.ghhud.api.v1.dispatchResponse
import io.github.jflowers.ghhud.api.v1.enqueueResponse
import io.github.jflowers.ghhud.api.v1.item
import io.github.jflowers.ghhud.api.v1.menuChoice
import io.github.jflowers.ghhud.api.v1.menuChoicesResponse
import io.github.jflowers.ghhud.api.v1.queueChoicesResponse
import io.github.jflowers.ghhud.api.v1.reinspectResponse
import io.github.jflowers.ghhud.api.v1.run as hubRun
import io.github.jflowers.ghhud.api.v1.snapshot
import com.google.protobuf.timestamp
import java.time.Instant
import kotlin.time.Duration.Companion.milliseconds
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
import org.junit.Assert.assertNull
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

        ok(menuChoicesResponse { why = "an agent is already running on it (fix, lap2:r@hoopoe)" })
        assertEquals(
            Answer.No("Nothing can start on acme/r#7 now: an agent is already running on it (fix, lap2:r@hoopoe)."),
            ItemCommands.choices(hub, row, queue = false),
        )
        ok(menuChoicesResponse {}) // a hub too old to say why
        assertEquals(
            Answer.No("Nothing can start on acme/r#7 now: no agent task fits it, or no checkout of acme/r is free."),
            ItemCommands.choices(hub, row, queue = false),
        )
        refused("dispatch is off")
        assertEquals(Answer.No("dispatch is off"), ItemCommands.run(hub, row, AgentChoice("fix", "x"), queue = false))
    }

    private fun cutOff(code: String) = server.enqueue(
        MockResponse.Builder().code(504).setHeader("Content-Type", "application/json")
            .body("""{"code":"$code","message":"timeout"}""").build(),
    )

    @Test
    fun aCutOffStartMayStillBeStarting() = runBlocking {
        // The hub never answered: no run shows up, so it may still be
        // starting, and it says not to try again blind.
        cutOff("deadline_exceeded")
        val said = ItemCommands.run(hub, row, AgentChoice("review", "zero-dot-force@zinnia"), queue = false, confirm = 200.milliseconds)
        assertEquals(
            Answer.No(
                "The hub didn't answer in time, so review may still be starting on zero-dot-force@zinnia. " +
                    "Check Agents before you try again.",
                unsure = true,
            ),
            said,
        )
        // A refusal is sure: said as the hub said it.
        refused("dispatch is off")
        assertEquals(Answer.No("dispatch is off"), ItemCommands.run(hub, row, AgentChoice("fix", "x"), queue = false))
    }

    @Test
    fun findsTheRunACutOffStartMade() {
        val since = Instant.ofEpochSecond(1_000)
        fun run(ref: String, task: String, at: Long) = hubRun {
            this.ref = ref
            this.task = task
            machine = "zinnia"
            workspace = "vc-2"
            startedAt = timestamp { seconds = at }
        }
        val s = snapshot {
            runs += run("acme/r#7", "review", 500) // an earlier run
            runs += run("acme/r#8", "review", 1_005) // another PR
            runs += run("acme/r#7", "fix", 1_005) // another task
        }
        assertNull(ItemCommands.startedRun(s, "acme/r#7", "review", since))
        val now = s.toBuilder().addRuns(run("acme/r#7", "review", 990)).build() // the hub's clock a little behind
        assertEquals("vc-2", ItemCommands.startedRun(now, "acme/r#7", "review", since)?.workspace)
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
    fun startsOneYouName() = runBlocking {
        ok(
            itemChoicesResponse {
                item = item {
                    kind = ItemKind.ITEM_KIND_PR
                    url = "https://github.com/acme/r/pull/9"
                    repo = "acme/r"
                    num = 9
                    title = "nine"
                }
                choices += menuChoice { task = "ci"; target = "oak"; suggested = true }
                choices += menuChoice { task = "rebase"; target = "oak" }
            },
        )
        val found = ItemCommands.lookUp(hub, "  https://github.com/acme/r/pull/9/files ")
        val named = NamedItem("https://github.com/acme/r/pull/9", "acme/r#9", "nine", isPR = true)
        assertEquals(Answer.Ok(named to listOf(AgentChoice("ci", "oak", suggested = true), AgentChoice("rebase", "oak"))), found)
        val req = ItemChoicesRequest.parseFrom(server.takeRequest().body!!.toByteArray())
        assertEquals("https://github.com/acme/r/pull/9/files", req.ref)
        assertEquals("PR acme/r#9 · nine", named.line)

        ok(dispatchResponse { task = "rebase"; workspace = "oak" })
        assertEquals(Answer.Ok("Started rebase on acme/r#9 in local:oak"), ItemCommands.run(hub, named, AgentChoice("rebase", "oak")))
        val d = DispatchRequest.parseFrom(server.takeRequest().body!!.toByteArray())
        assertEquals("rebase" to "https://github.com/acme/r/pull/9", d.task to d.url)

        ok(
            itemChoicesResponse {
                item = item { repo = "acme/r"; num = 3; kind = ItemKind.ITEM_KIND_ISSUE }
                why = "a person handles it (the human-gate label)"
            },
        )
        assertEquals(
            Answer.No("Nothing can start on acme/r#3 now: a person handles it (the human-gate label)."),
            ItemCommands.lookUp(hub, "acme/r#3"),
        )
        ok(itemChoicesResponse { item = item { repo = "acme/r"; num = 3; kind = ItemKind.ITEM_KIND_ISSUE } })
        assertEquals(
            Answer.No("Nothing can start on acme/r#3 now: no checkout of acme/r is free, or a person handles it (human-gate)."),
            ItemCommands.lookUp(hub, "acme/r#3"),
        )
        assertEquals(Answer.No("Name an issue or PR"), ItemCommands.lookUp(hub, "  "))
    }

    @Test
    fun findsASharedLink() {
        assertEquals(
            "https://github.com/acme/r/pull/12",
            ItemCommands.sharedLink("Fix the thing by ann · Pull Request #12 · acme/r https://github.com/acme/r/pull/12"),
        )
        assertEquals("https://github.com/acme/r/issues/4#x", ItemCommands.sharedLink("https://github.com/acme/r/issues/4#x"))
        assertEquals(null, ItemCommands.sharedLink("https://github.com/acme/r"))
        assertEquals(null, ItemCommands.sharedLink("acme/r#4"))
    }

    @Test
    fun offWithoutAHub() {
        assertEquals("Not connected to the hub", ItemCommands.off(HubState(HubStatus.Connecting)))
        assertEquals("Dispatch is off on the hub", ItemCommands.off(HubState(HubStatus.Connected)))
    }
}
