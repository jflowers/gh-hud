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
import io.github.jflowers.ghhud.api.v1.ToolkitAction
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.DeweyPass
import com.google.protobuf.Timestamp
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
import io.github.jflowers.ghhud.core.PaneRead
import io.github.jflowers.ghhud.core.PlanItem
import io.github.jflowers.ghhud.core.RolloutRow
import io.github.jflowers.ghhud.core.WatchTarget
import io.github.jflowers.ghhud.api.v1.TranscriptResponse
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
        override suspend fun dewey(machine: String?, force: Boolean): Answer<String> {
            asked += "dewey ${machine ?: "all"} force=$force"
            return Answer.No("dewey_reindex is off")
        }
        override suspend fun deweyCancel(machine: String?): Answer<String> {
            asked += "cancel ${machine ?: "all"}"
            return Answer.Ok("Dewey reindex pass cancelled on ${machine ?: "all"}")
        }
        override fun updateFleet(change: (ListState<FleetSort>) -> ListState<FleetSort>) { state.value = change(state.value) }
    }

    private fun show(status: HubStatus = HubStatus.Connected, snap: Snapshot = this.snap) {
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

    /** A Dewey reindex pass on chai, running (#139). */
    private fun passing(): Snapshot {
        val b = snap.toBuilder()
        val f = b.fleet.toBuilder()
        val i = f.machinesList.indexOfFirst { it.name == "chai" }
        f.setMachines(
            i,
            f.getMachines(i).toBuilder().setDeweyPass(
                DeweyPass.newBuilder().setRunning(true).setDone(3).setTotal(12)
                    .setStarted(Timestamp.newBuilder().setSeconds(System.currentTimeMillis() / 1000 - 60)),
            ),
        )
        return b.setFleet(f).build()
    }

    @Test
    fun reindexingAsksFirstAndSaysWhyNot() {
        show(snap = passing())
        compose.onNodeWithTag("dewey-line:chai:0", useUnmergedTree = true).assertTextContains("dewey pass: reindexing 3/12", substring = true)
        compose.onNodeWithTag("dewey-all").performClick()
        compose.onNodeWithTag("dewey-ask").assertTextEquals("Reindex the Dewey vaults that need it on every machine?")
        compose.onNodeWithTag("dewey-force").performClick()
        compose.onNodeWithTag("dewey-ask").assertTextEquals("Reindex every Dewey vault on every machine?")
        assertEquals(emptyList<String>(), asked)
        compose.onNodeWithTag("dewey-confirm").performClick()
        compose.waitUntil(5_000) { said.isNotEmpty() }
        assertEquals(listOf("dewey all force=true"), asked)
        assertEquals(listOf("dewey_reindex is off bad=true"), said)

        compose.onNodeWithTag("machine:chai").performClick()
        compose.onNodeWithTag("dewey-cancel:chai").performClick()
        compose.waitUntil(5_000) { said.size == 2 }
        assertEquals("cancel chai", asked.last())
    }

    /** The app shows the Fleet place only while the hub runs a fleet. */
    @Test
    fun theFleetPlaceComesAndGoes() {
        val shown = mutableStateOf(snap)
        compose.setContent {
            val s = shown.value
            MaterialTheme {
                AppScreen(Ui(HubAddress("https://hub"), HubState(HubStatus.Connected, s, Attention.of(s))), ItemsView(), AgentsView(),
                    ListState(FleetSort.HUB), NoopActions)
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

