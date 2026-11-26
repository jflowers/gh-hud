package io.github.jflowers.ghhud

import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertTextContains
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.google.protobuf.timestamp
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.TranscriptResponse
import io.github.jflowers.ghhud.api.v1.transcriptMessage
import io.github.jflowers.ghhud.api.v1.transcriptPart
import io.github.jflowers.ghhud.api.v1.transcriptResponse
import io.github.jflowers.ghhud.core.AgentChoice
import io.github.jflowers.ghhud.core.AgentList
import io.github.jflowers.ghhud.core.AgentRow
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.ItemFilter
import io.github.jflowers.ghhud.core.ItemList
import io.github.jflowers.ghhud.core.ItemRow
import io.github.jflowers.ghhud.core.PaneRead
import io.github.jflowers.ghhud.core.WatchTarget
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.time.Instant

/** Runs on a device: Watch, Transcript, and the buttons that open them. */
@RunWith(AndroidJUnit4::class)
class ViewerScreensTest {
    @get:Rule
    val compose = createComposeRule()

    private val snap: Snapshot = InstrumentationRegistry.getInstrumentation().context.assets
        .open("dashboard.binpb").use { Snapshot.parseFrom(it) }
    private val now: Instant = Instant.ofEpochSecond(snap.fetchedAt.seconds)
    private val reads = mutableListOf<String>()

    private val act = object : ViewerActions {
        override fun follow(t: WatchTarget): Flow<PaneRead> = flow {
            var n = 0
            while (true) {
                n++
                reads += t.key
                emit(if (n == 2) PaneRead(null, "herdr: no such pane", Instant.now()) else PaneRead("\$ go test\nok $n", null, Instant.now()))
                delay(50)
            }
        }
        override suspend fun transcript(runId: String): Answer<TranscriptResponse> =
            if (runId == "bad") {
                Answer.No("no opencode session linked to this run")
            } else {
                Answer.Ok(
                    transcriptResponse {
                        omitted = 12
                        messages += transcriptMessage {
                            role = "user"
                            created = timestamp { seconds = 1_794_767_756 }
                            parts += transcriptPart { text = "Fix the flaky test." }
                        }
                        messages += transcriptMessage {
                            role = "assistant"
                            parts += transcriptPart { text = "Running it." }
                            parts += transcriptPart { tool = "bash"; text = "go test ./..."; failed = true }
                        }
                    },
                )
            }
    }

    @Test
    fun watchFollowsAndPauses() {
        compose.setContent { MaterialTheme { WatchScreen(WatchTarget("run-3", "", "review acme/widgets#401"), act, onBack = {}) } }
        compose.waitUntil(5_000) { reads.size >= 3 }
        compose.onNodeWithTag("viewer-title").assertTextEquals("Watch · review acme/widgets#401")
        compose.onNodeWithTag("pane").assertTextContains("\$ go test", substring = true)
        compose.onNodeWithTag("pause").performClick()
        compose.onNodeWithTag("watch-status").assertTextEquals("Paused")
        val paused = reads.size
        Thread.sleep(300)
        compose.waitForIdle()
        assertEquals("no reads while paused", paused, reads.size)
        assertEquals("run:run-3", reads.first())
    }

    @Test
    fun aTranscript() {
        compose.setContent { MaterialTheme { TranscriptScreen("run-0", "fix acme/api#70", act, onBack = {}) } }
        compose.waitUntil(5_000) { compose.onAllNodesWithTagExists("transcript") }
        compose.onNodeWithText("Fix the flaky test.").assertIsDisplayed()
        compose.onNodeWithText("▸ bash  go test ./...  (failed)").assertIsDisplayed()
        compose.onNodeWithTag("omitted").assertTextEquals("12 earlier messages not shown")
    }

    @Test
    fun noTranscriptSaysWhy() {
        compose.setContent { MaterialTheme { TranscriptScreen("bad", "x", act, onBack = {}) } }
        compose.waitUntil(5_000) { compose.onAllNodesWithTagExists("transcript-error") }
        compose.onNodeWithTag("transcript-error").assertTextEquals("Can't read the transcript: no opencode session linked to this run")
    }

    private val opened = mutableListOf<String>()
    private val items = object : ItemActions {
        override fun open(url: String) {}
        override fun copy(text: String) {}
        override suspend fun choices(row: ItemRow, queue: Boolean): Answer<List<AgentChoice>> = Answer.Ok(emptyList())
        override suspend fun run(row: ItemRow, c: AgentChoice, queue: Boolean): Answer<String> = Answer.Ok("")
        override suspend fun recheck(row: ItemRow): Answer<String> = Answer.Ok("")
    }
    private val agentAct = object : AgentActions {
        override suspend fun recheck(r: AgentRow): Answer<String> = Answer.Ok("")
        override suspend fun checkoutAction(
            h: io.github.jflowers.ghhud.api.v1.CheckoutHealth,
            kind: io.github.jflowers.ghhud.api.v1.CheckoutActionKind,
        ): Answer<String> = Answer.Ok("")
    }

    private fun agent(id: String) = AgentList.rows(snap, showStale = true, now = now).first { it.id == id }

    @Test
    fun aLiveAgentOffersWatchAndItsTranscript() {
        compose.setContent {
            MaterialTheme {
                AgentScreen(agent("run-3"), null, agentAct, items, say = { _, _ -> }, onBack = {},
                    onWatch = { opened += "watch ${it.key}" }, onTranscript = { id, _ -> opened += "transcript $id" }, now = now)
            }
        }
        compose.onNodeWithTag("watch").performClick()
        compose.onNodeWithTag("transcript").performClick()
        assertEquals(listOf("watch run:run-3", "transcript run-3"), opened)
    }

    @Test
    fun aFinishedRunHasNothingToWatch() {
        compose.setContent {
            MaterialTheme { AgentScreen(agent("run-0"), null, agentAct, items, say = { _, _ -> }, onBack = {}, now = now) }
        }
        compose.onNodeWithTag("watch").assertDoesNotExist()
        compose.onNodeWithTag("transcript").assertIsDisplayed()
    }

    @Test
    fun anItemsLiveRun() {
        val row = ItemList.rows(snap, ItemFilter.ALL, now = now).first { it.ref == "acme/widgets#401" }
        compose.setContent {
            MaterialTheme {
                ItemScreen(row, null, items, say = { _, _ -> }, onBack = {}, onWatch = { opened += "watch ${it.key}" },
                    onTranscript = { id, t -> opened += "transcript $id $t" }, now = now)
            }
        }
        compose.onNodeWithTag("watch").performClick()
        compose.onNodeWithTag("transcript").performClick()
        assertEquals(listOf("watch run:run-3", "transcript run-3 review acme/widgets#401"), opened)
    }

    private fun androidx.compose.ui.test.junit4.ComposeContentTestRule.onAllNodesWithTagExists(tag: String) =
        onAllNodes(androidx.compose.ui.test.hasTestTag(tag)).fetchSemanticsNodes().isNotEmpty()
}
