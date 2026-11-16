package io.github.jflowers.ghhud

import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.hasTestTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.ToolkitAction
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.Attention
import io.github.jflowers.ghhud.core.HubAddress
import io.github.jflowers.ghhud.core.HubState
import io.github.jflowers.ghhud.core.HubStatus
import io.github.jflowers.ghhud.core.RolloutRow
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/** Runs on a device: the Tools screen from the shared dashboard fixture. */
@RunWith(AndroidJUnit4::class)
class ToolkitScreenTest {
    @get:Rule
    val compose = createComposeRule()

    private val snap: Snapshot =
        InstrumentationRegistry.getInstrumentation().context.assets.open("dashboard.binpb").use { Snapshot.parseFrom(it) }
    private val asked = mutableListOf<String>()
    private val said = mutableListOf<String>()
    private val opened = mutableListOf<String>()

    private val act = object : ToolkitActions {
        override suspend fun checkTools(): Answer<String> = did("check")
        override suspend fun rollOut(tool: String, version: String) = did("rollout $tool $version")
        override suspend fun decline(id: String) = did("decline $id")
        override suspend fun toolkit(action: ToolkitAction, r: RolloutRow) = did("${action.name} ${r.id}")
        override fun open(url: String) {
            opened += url
        }

        private fun did(what: String): Answer<String> {
            asked += what
            return Answer.Ok("did $what")
        }
    }

    private fun show(open: String? = null) {
        val ui = Ui(HubAddress("https://hub"), HubState(HubStatus.Connected, snap, Attention.of(snap)))
        compose.setContent {
            MaterialTheme { ToolkitScreen(ui, act, say = { t, bad -> said += "$t bad=$bad" }, refresh = {}, open = open) }
        }
    }

    @Test
    fun toolsAndRollouts() {
        show()
        compose.onNodeWithTag("toolkit-state").assertTextEquals("checked 2h ago · 1 rolling out")
        compose.onNodeWithTag("tool-mark:uf", useUnmergedTree = true).assertTextEquals("◆")
        compose.onNodeWithTag("tool-mark:dewey", useUnmergedTree = true).assertTextEquals("↑")
        compose.onNodeWithTag("rollout-counts:gaze@2.4.0", useUnmergedTree = true).assertTextEquals("1/3 landed · 1 held · 1 refused")
        // The newest rollout is open: its repos, and a tap opens a PR.
        compose.onNodeWithTag("target-state:acme/agents", useUnmergedTree = true).assertTextEquals("held")
        compose.onNodeWithTag("target:acme/agents").performClick()
        assertEquals(listOf("https://github.com/acme/agents/pull/3"), opened)
        compose.onNodeWithTag("cancel:gaze@2.4.0").performClick()
        compose.waitUntil(5_000) { asked.isNotEmpty() }
        assertEquals(listOf("TOOLKIT_ACTION_CANCEL gaze@2.4.0"), asked)
    }

    @Test
    fun aWaitingReleaseShowsItsNotes() {
        show()
        compose.onNodeWithTag("tool:uf").performClick()
        compose.onNodeWithTag("release-notes").assertTextEquals("## Fixes\n- faster")
        compose.onNodeWithTag("release-approve").performClick()
        compose.waitUntil(5_000) { asked.isNotEmpty() }
        assertEquals(listOf("rollout uf 0.22.0"), asked)
        compose.onNodeWithTag("release-sheet").assertDoesNotExist()
    }

    @Test
    fun declineAndRollOut() {
        show()
        compose.onNodeWithTag("tool:uf").performClick()
        compose.onNodeWithTag("release-decline").performClick()
        compose.waitUntil(5_000) { asked.size == 1 }
        // dewey has a newer release nobody proposed: rolling it out asks first.
        compose.onNodeWithTag("tool:dewey").performClick()
        compose.onNodeWithText("Roll out dewey 3.6.0?").assertIsDisplayed()
        compose.onNodeWithTag("rollout-confirm").performClick()
        compose.waitUntil(5_000) { asked.size == 2 }
        assertEquals(listOf("decline uf@0.22.0", "rollout dewey 3.6.0"), asked)
    }

    @Test
    fun aFinishedRolloutRetriesOrGoes() {
        show(open = "dewey@3.6.0")
        compose.onNodeWithTag("toolkit").performScrollToNode(hasTestTag("retry:dewey@3.6.0"))
        compose.onNodeWithTag("target-state:acme/app", useUnmergedTree = true).assertTextEquals("failed")
        compose.onNodeWithTag("retry:dewey@3.6.0").performClick()
        compose.waitUntil(5_000) { asked.isNotEmpty() }
        compose.onNodeWithTag("dismiss:dewey@3.6.0").performClick()
        compose.waitUntil(5_000) { asked.size == 2 }
        assertEquals(listOf("TOOLKIT_ACTION_RETRY dewey@3.6.0", "TOOLKIT_ACTION_DISMISS dewey@3.6.0"), asked)
        compose.onNodeWithTag("check-tools").performClick()
        compose.waitUntil(5_000) { asked.size == 3 }
        assertEquals("check", asked.last())
    }

    /** A rollout's repo with no PR, in the inbox, opens the Tools screen on its rollout. */
    @Test
    fun theInboxOpensTools() {
        val shown = mutableStateOf(snap)
        compose.setContent {
            val s = shown.value
            MaterialTheme {
                AppScreen(Ui(HubAddress("https://hub"), HubState(HubStatus.Connected, s, Attention.of(s))), ItemsView(), AgentsView(),
                    ListState(io.github.jflowers.ghhud.core.FleetSort.HUB), NoopActions)
            }
        }
        compose.onNodeWithTag("sections").performScrollToNode(hasTestTag("row:rollout:gaze@2.4.0 acme/lib"))
        compose.onNodeWithTag("row:rollout:gaze@2.4.0 acme/lib").performClick()
        compose.onNodeWithTag("toolkit").assertIsDisplayed()
        compose.onNodeWithTag("target-state:acme/lib", useUnmergedTree = true).assertTextEquals("refused")
    }
}
