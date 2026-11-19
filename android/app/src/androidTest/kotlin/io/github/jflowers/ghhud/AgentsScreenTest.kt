package io.github.jflowers.ghhud

import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertTextContains
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performImeAction
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.test.performTouchInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.CheckoutHealth
import com.google.protobuf.Timestamp
import io.github.jflowers.ghhud.core.AgentChoice
import io.github.jflowers.ghhud.core.AgentList
import io.github.jflowers.ghhud.core.AgentRow
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.Attention
import io.github.jflowers.ghhud.core.ColumnFilter
import io.github.jflowers.ghhud.core.HubAddress
import io.github.jflowers.ghhud.core.HubState
import io.github.jflowers.ghhud.core.HubStatus
import io.github.jflowers.ghhud.core.ItemRow
import io.github.jflowers.ghhud.core.WorkspaceList
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.time.Instant

/** Runs on a device: Agents and Workspaces from the shared dashboard fixture. */
@RunWith(AndroidJUnit4::class)
class AgentsScreenTest {
    @get:Rule
    val compose = createComposeRule()

    private val snap: Snapshot = InstrumentationRegistry.getInstrumentation().context.assets
        .open("dashboard.binpb").use { Snapshot.parseFrom(it) }
    private val now: Instant = Instant.ofEpochSecond(snap.fetchedAt.seconds)
    private val ui = Ui(HubAddress("https://hub"), HubState(HubStatus.Connected, snap, Attention.of(snap)))

    private val state = mutableStateOf(AgentsView())
    private val asked = mutableListOf<String>()
    private val said = mutableListOf<String>()

    private val ctl = object : AgentsControl {
        override fun updateAgents(change: (AgentsView) -> AgentsView) { state.value = change(state.value) }
        override fun refreshAgents() { asked += "refresh agents" }
        override fun refreshWorkspaces() { asked += "refresh workspaces" }
    }

    private fun list() = compose.setContent {
        MaterialTheme {
            AgentsScreen(ui, state.value, ctl, onOpenAgent = { asked += "open agent ${it.id}" },
                onOpenWorkspace = { asked += "open workspace ${it.place}" }, now = now)
        }
    }

    @Test
    fun agentsLiveAndStale() {
        list()
        compose.onNodeWithTag("tab:agents").assertTextContains("3 running", substring = true)
        compose.onNodeWithTag("agent:ses-stale").assertDoesNotExist()
        compose.onNodeWithTag("show-stale").performClick()
        compose.onNodeWithTag("agent:ses-stale").assertIsDisplayed()
        compose.onNodeWithTag("live-only").performClick()
        compose.onNodeWithTag("agent:ses-failed").assertDoesNotExist()
        compose.onNodeWithTag("agent:run-3").performClick()
        assertEquals(listOf("open agent run-3"), asked)
    }

    @Test
    fun searchAndHide() {
        list()
        compose.onNodeWithTag("search").performTextReplacement("where:chai")
        compose.onNodeWithTag("search").performImeAction()
        assertEquals(listOf(ColumnFilter.of("where", "chai")), state.value.agents.kept)
        compose.onNodeWithTag("agent:ses-blocked").assertDoesNotExist()
        compose.onNodeWithTag("kept:where:chai").performClick()
        compose.onNodeWithTag("agent:ses-failed").performTouchInput { longClick() }
        compose.onNodeWithText("Hide status: failed").performClick()
        compose.onNodeWithTag("agent:ses-failed").assertDoesNotExist()
    }

    @Test
    fun workspacesReadAgainWhenOld() {
        state.value = AgentsView(tab = AgentsTab.WORKSPACES)
        list()
        compose.onNodeWithTag("tab:workspaces").assertTextContains("2 ready", substring = true)
        compose.waitForIdle()
        // The fixture's checkouts are 10s old at its own time: fresh enough.
        assertEquals(emptyList<String>(), asked)
        compose.onNodeWithTag("workspace:api-2").performClick()
        assertEquals(listOf("open workspace api-2"), asked)
    }

    /** A checkout whose index Dewey says to rebuild (#139). */
    private fun oldIndex(): Snapshot {
        val b = snap.toBuilder()
        val i = b.workspacesList.indexOfFirst { it.workspace == "api" }
        b.setWorkspaces(
            i,
            b.getWorkspaces(i).toBuilder()
                .setIndex("reindex needed: index format 0, this Dewey writes 1, no embeddings")
                .addAllIndexReasons(listOf("index format 0, this Dewey writes 1", "no embeddings"))
                .setIndexBuilt(Timestamp.newBuilder().setSeconds(now.epochSecond - 3 * 86400))
                .build(),
        )
        return b.build()
    }

    @Test
    fun aCheckoutsDeweyIndexInTheList() {
        val s = oldIndex()
        compose.setContent {
            MaterialTheme {
                AgentsScreen(Ui(HubAddress("https://hub"), HubState(HubStatus.Connected, s, Attention.of(s))),
                    AgentsView(tab = AgentsTab.WORKSPACES), ctl, onOpenAgent = {}, onOpenWorkspace = {}, now = now)
            }
        }
        compose.onNodeWithTag("index:api", useUnmergedTree = true).assertTextEquals("dewey old format +1 3d")
    }

    @Test
    fun aCheckoutsDeweyIndexOnItsScreen() {
        val row = WorkspaceList.rows(oldIndex()).first { it.place == "api" }
        compose.setContent { MaterialTheme { WorkspaceScreen(row, items, onBack = {}, now = now) } }
        compose.onNodeWithTag("fact:index").assertTextEquals("reindex needed (still dispatchable)")
        compose.onNodeWithText("index format 0, this Dewey writes 1").assertExists()
        compose.onNodeWithText("no embeddings").assertExists()
    }

    private val items = object : ItemActions {
        override fun open(url: String) { asked += "open $url" }
        override fun copy(text: String) { asked += "copy $text" }
        override suspend fun choices(row: ItemRow, queue: Boolean): Answer<List<AgentChoice>> = Answer.Ok(emptyList())
        override suspend fun run(row: ItemRow, c: AgentChoice, queue: Boolean): Answer<String> = Answer.Ok("")
        override suspend fun recheck(row: ItemRow): Answer<String> = Answer.Ok("")
    }
    private val act = object : AgentActions {
        override suspend fun recheck(r: AgentRow): Answer<String> {
            asked += "recheck ${r.id}"
            return Answer.Ok("Re-checked fix on acme/api#70: succeeded")
        }
    }

    private fun agent(id: String) {
        val row = AgentList.rows(snap, showStale = true, now = now).first { it.id == id }
        compose.setContent { MaterialTheme { AgentScreen(row, null, act, items, say = { t, bad -> said += "$t bad=$bad" }, onBack = {}, now = now) } }
    }

    @Test
    fun aFinishedDispatch() {
        agent("run-0")
        compose.onNodeWithTag("headline").assertTextEquals("succeeded")
        compose.onNodeWithTag("fact:took").assertTextEquals("1h15")
        compose.onNodeWithTag("final").assertTextEquals("No final message recorded.")
        compose.onNodeWithTag("recheck").performClick()
        compose.waitUntil(5_000) { said.size == 2 }
        assertEquals(listOf("Re-checking fix on acme/api#70… bad=false", "Re-checked fix on acme/api#70: succeeded bad=false"), said)
        compose.onNodeWithTag("open").performClick()
        assertEquals(listOf("recheck run-0", "open https://github.com/acme/api/issues/70"), asked)
    }

    @Test
    fun aRunningSessionHasNothingToRecheck() {
        agent("run-3")
        compose.onNodeWithTag("headline").assertTextEquals("running")
        compose.onNodeWithTag("recheck").assertDoesNotExist()
    }

    @Test
    fun aCheckout() {
        val row = WorkspaceList.rows(snap).first { it.place == "api" }
        compose.setContent { MaterialTheme { WorkspaceScreen(row, items, onBack = {}) } }
        compose.onNodeWithTag("sentence").assertTextEquals("Ready. A run here switches to main and fast-forwards it.")
        compose.onNodeWithTag("fact:stashes").assertTextEquals("1")
        compose.onNodeWithText("feature: 3 commits").assertIsDisplayed()
        compose.onNodeWithText("Copy directory").performClick()
        assertEquals(listOf("copy /w/api"), asked)
    }
}
