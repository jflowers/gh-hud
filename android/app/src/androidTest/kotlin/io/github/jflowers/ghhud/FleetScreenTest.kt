package io.github.jflowers.ghhud

import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.assertTextContains
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performImeAction
import androidx.compose.ui.test.performTextReplacement
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.jflowers.ghhud.api.v1.FleetAction
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.core.AgentChoice
import io.github.jflowers.ghhud.core.AgentRow
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.Attention
import io.github.jflowers.ghhud.core.AttentionRow
import io.github.jflowers.ghhud.core.CaseRow
import io.github.jflowers.ghhud.core.EpicRow
import io.github.jflowers.ghhud.core.FleetSort
import io.github.jflowers.ghhud.core.HubAddress
import io.github.jflowers.ghhud.core.HubState
import io.github.jflowers.ghhud.core.HubStatus
import io.github.jflowers.ghhud.core.ItemRow
import io.github.jflowers.ghhud.core.PlanItem
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/** Runs on a device: the Fleet from the shared dashboard fixture. */
@RunWith(AndroidJUnit4::class)
class FleetScreenTest {
    @get:Rule
    val compose = createComposeRule()

    private fun fixture(name: String): Snapshot =
        InstrumentationRegistry.getInstrumentation().context.assets.open(name).use { Snapshot.parseFrom(it) }

    private val snap = fixture("dashboard.binpb")
    private val state = mutableStateOf(ListState(FleetSort.HUB))
    private val asked = mutableListOf<String>()
    private val said = mutableListOf<String>()

    private val act = object : FleetActions {
        override suspend fun fleet(action: FleetAction): Answer<String> {
            asked += action.name
            return Answer.Ok("did $action")
        }
        override fun updateFleet(change: (ListState<FleetSort>) -> ListState<FleetSort>) { state.value = change(state.value) }
    }

    private fun show(status: HubStatus = HubStatus.Connected) {
        val ui = Ui(HubAddress("https://hub"), HubState(status, snap, Attention.of(snap)))
        compose.setContent { MaterialTheme { FleetScreen(ui, state.value, act, say = { t, bad -> said += "$t bad=$bad" }, refresh = {}) } }
    }

    @Test
    fun theRollout() {
        show()
        compose.onNodeWithTag("fleet-state").assertTextEquals("failed on oak")
        compose.onNodeWithTag("fleet-ci").assertTextEquals("green")
        compose.onNodeWithTag("machine-state:chai", useUnmergedTree = true).assertTextEquals("draining")
        compose.onNodeWithTag("retry").assertTextContains("Retry oak", substring = true).performClick()
        compose.onNodeWithTag("pause").assertTextContains("Pause", substring = true).performClick()
        compose.waitUntil(5_000) { said.size == 2 }
        assertEquals(listOf("FLEET_ACTION_RETRY", "FLEET_ACTION_PAUSE"), asked)
    }

    @Test
    fun skippingTheWaitAsksFirst() {
        show()
        compose.onNodeWithTag("skip-wait").assertIsEnabled().performClick()
        compose.onNodeWithText("Skip the drain wait?").assertIsDisplayed()
        assertEquals(emptyList<String>(), asked)
        compose.onNodeWithTag("skip-confirm").performClick()
        compose.waitUntil(5_000) { asked.isNotEmpty() }
        assertEquals(listOf("FLEET_ACTION_SKIP_WAIT"), asked)
    }

    @Test
    fun searchAndOffline() {
        show(HubStatus.Lost("refused"))
        compose.onNodeWithTag("off").assertTextEquals("Not connected to the hub")
        compose.onNodeWithTag("pause").assertIsNotEnabled()
        compose.onNodeWithTag("search").performTextReplacement("state:failed")
        compose.onNodeWithTag("search").performImeAction()
        compose.onNodeWithTag("machine:oak").assertIsDisplayed()
        compose.onNodeWithTag("machine:chai").assertDoesNotExist()
    }

    /** The app shows the Fleet place only while the hub runs a fleet. */
    @Test
    fun theFleetPlaceComesAndGoes() {
        val shown = mutableStateOf(snap)
        val noop = object : AppActions {
            override fun open(url: String) {}
            override fun approve(row: AttentionRow) {}
            override fun reject(row: AttentionRow) {}
            override fun refresh() {}
            override fun setAddress(a: HubAddress) {}
            override fun setNotify(on: Boolean) {}
            override fun update(change: (ItemsView) -> ItemsView) {}
            override fun keepTerms() {}
            override fun copy(text: String) {}
            override suspend fun choices(row: ItemRow, queue: Boolean): Answer<List<AgentChoice>> = Answer.Ok(emptyList())
            override suspend fun run(row: ItemRow, c: AgentChoice, queue: Boolean): Answer<String> = Answer.Ok("")
            override suspend fun recheck(row: ItemRow): Answer<String> = Answer.Ok("")
            override suspend fun enqueue(r: PlanItem): Answer<String> = Answer.Ok("")
            override suspend fun move(r: PlanItem, by: Long): Answer<String> = Answer.Ok("")
            override suspend fun unqueue(r: PlanItem): Answer<String> = Answer.Ok("")
            override suspend fun approve(c: CaseRow): Answer<String> = Answer.Ok("")
            override suspend fun reject(c: CaseRow): Answer<String> = Answer.Ok("")
            override suspend fun approve(e: EpicRow): Answer<String> = Answer.Ok("")
            override suspend fun reread(e: EpicRow): Answer<String> = Answer.Ok("")
            override suspend fun drop(e: EpicRow): Answer<String> = Answer.Ok("")
            override suspend fun clearGate(epic: String, piece: String): Answer<String> = Answer.Ok("")
            override suspend fun enroll(target: String): Answer<String> = Answer.Ok("")
            override fun updateAgents(change: (AgentsView) -> AgentsView) {}
            override fun refreshAgents() {}
            override fun refreshWorkspaces() {}
            override suspend fun recheck(r: AgentRow): Answer<String> = Answer.Ok("")
            override suspend fun fleet(action: FleetAction): Answer<String> = Answer.Ok("")
            override fun updateFleet(change: (ListState<FleetSort>) -> ListState<FleetSort>) {}
        }
        compose.setContent {
            val s = shown.value
            MaterialTheme {
                AppScreen(Ui(HubAddress("https://hub"), HubState(HubStatus.Connected, s, Attention.of(s))), ItemsView(), AgentsView(),
                    ListState(FleetSort.HUB), noop)
            }
        }
        compose.onNodeWithText("1/6", useUnmergedTree = true).assertExists()
        compose.onNodeWithTag("place:FLEET").performClick()
        compose.onNodeWithTag("fleet-state").assertIsDisplayed()
        shown.value = fixture("readonly.binpb")
        compose.onNodeWithTag("place:FLEET").assertDoesNotExist()
        compose.onNodeWithTag("fleet-state").assertDoesNotExist()
    }
}

