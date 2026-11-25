package io.github.jflowers.ghhud

import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.test.hasTestTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.core.AgentChoice
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.Attention
import io.github.jflowers.ghhud.core.CaseRow
import io.github.jflowers.ghhud.core.EpicRow
import io.github.jflowers.ghhud.core.HubAddress
import io.github.jflowers.ghhud.core.HubState
import io.github.jflowers.ghhud.core.HubStatus
import io.github.jflowers.ghhud.core.ItemRow
import io.github.jflowers.ghhud.core.PlanItem
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/** Runs on a device: the Plan from the shared dashboard fixture, and what taps ask for. */
@RunWith(AndroidJUnit4::class)
class PlanScreenTest {
    @get:Rule
    val compose = createComposeRule()

    private val snap: Snapshot = InstrumentationRegistry.getInstrumentation().context.assets
        .open("dashboard.binpb").use { Snapshot.parseFrom(it) }

    private val asked = mutableListOf<String>()
    private val said = mutableListOf<String>()

    private val plan = object : PlanActions {
        private fun ok(what: String, say: String = what): Answer<String> { asked += what; return Answer.Ok(say) }
        override suspend fun enqueue(r: PlanItem) = ok("enqueue ${r.id}")
        override suspend fun move(r: PlanItem, by: Long) = ok("move ${r.id} $by", "")
        override suspend fun unqueue(r: PlanItem) = ok("unqueue ${r.id}")
        override suspend fun approve(c: CaseRow) = ok("approve case ${c.proposal}")
        override suspend fun reject(c: CaseRow): Answer<String> { asked += "reject case ${c.proposal}"; return Answer.No("refused") }
        override suspend fun approve(e: EpicRow) = ok("approve epic ${e.ref} ${e.planToApprove}")
        override suspend fun reread(e: EpicRow) = ok("reread ${e.ref}")
        override suspend fun drop(e: EpicRow) = ok("drop ${e.ref}")
        override suspend fun finish(e: EpicRow) = ok("finish ${e.ref} ${e.planToFinish}")
        override suspend fun clearGate(epic: String, piece: String) = ok("clear $epic $piece")
        override suspend fun enroll(target: String): Answer<String> {
            asked += "enroll $target"
            return if (target == "bad") Answer.No("not an issue") else Answer.Ok("Enrolled $target")
        }
    }
    private val items = object : ItemActions {
        override fun open(url: String) { asked += "open $url" }
        override fun copy(text: String) { asked += "copy $text" }
        override suspend fun choices(row: ItemRow, queue: Boolean): Answer<List<AgentChoice>> = Answer.Ok(emptyList())
        override suspend fun run(row: ItemRow, c: AgentChoice, queue: Boolean): Answer<String> = Answer.Ok("")
        override suspend fun recheck(row: ItemRow): Answer<String> = Answer.Ok("")
    }

    private fun show(dispatch: Boolean = true) {
        val s = snap.toBuilder().setDispatch(dispatch).build()
        val ui = Ui(HubAddress("https://hub"), HubState(HubStatus.Connected, s, Attention.of(s)))
        compose.setContent { MaterialTheme { PlanScreen(ui, plan, items, say = { t, bad -> said += "$t bad=$bad" }, refresh = {}) } }
    }

    private fun scrollTo(tag: String) = compose.onNodeWithTag("plan").performScrollToNode(hasTestTag(tag))

    @Test
    fun casesAndEpics() {
        show()
        compose.onNodeWithTag("plan-summary").assertTextEquals("4 start next · slots chai 1/2, local 0/1, oak —")
        compose.onNodeWithTag("approve-case:shared_failure|acme/widgets|lint").performClick()
        compose.onNodeWithTag("reject-case:shared_failure|acme/widgets|lint").performClick()
        compose.waitUntil(5_000) { said.size == 2 }
        assertEquals(listOf("approve case p1", "reject case p1"), asked)
        assertEquals("refused bad=true", said[1])

        asked.clear()
        scrollTo("approve-epic:acme/api#90")
        // What the plan changed since its last approval sits above Approve;
        // a stale plan shows none until it's reread.
        compose.onNodeWithTag("changes:acme/api#90").assertExists()
        compose.onNodeWithText("± #91 waits on nothing (was #92)").assertExists()
        compose.onNodeWithTag("changes:acme/api#95").assertDoesNotExist()
        compose.onNodeWithTag("approve-epic:acme/api#90").performClick()
        scrollTo("clear:acme/widgets#63")
        compose.onNodeWithTag("clear:acme/widgets#63").performClick()
        compose.waitUntil(5_000) { asked.size == 2 }
        assertEquals(listOf("approve epic acme/api#90 plan-90", "clear acme/widgets#60 acme/widgets#63"), asked)
    }

    @Test
    fun droppingAnEpicAsksFirst() {
        show()
        scrollTo("epic-menu:acme/docs#7")
        compose.onNodeWithTag("epic-menu:acme/docs#7").performClick()
        compose.onNodeWithTag("drop:acme/docs#7").performClick()
        compose.onNodeWithText("Stop following acme/docs#7?").assertIsDisplayed()
        assertEquals(emptyList<String>(), asked)
        compose.onNodeWithTag("drop-confirm").performClick()
        compose.waitUntil(5_000) { asked.isNotEmpty() }
        assertEquals(listOf("drop acme/docs#7"), asked)
    }

    @Test
    fun yourQueue() {
        show()
        scrollTo("up:fix|acme/api#70")
        compose.onNodeWithTag("plan-status:fix|acme/api#70", useUnmergedTree = true).assertTextEquals("no free slot on oak")
        compose.onNodeWithTag("up:fix|acme/api#70").performClick()
        compose.waitUntil(5_000) { asked.isNotEmpty() }
        compose.onNodeWithTag("plan-row:fix|acme/api#70").performClick()
        compose.onNodeWithTag("unqueue:fix|acme/api#70").performClick()
        scrollTo("plan-row:fix|acme/api#71")
        compose.onNodeWithTag("plan-row:fix|acme/api#71").performClick()
        compose.onNodeWithTag("enqueue:fix|acme/api#71").performClick()
        compose.waitUntil(5_000) { asked.size == 3 }
        assertEquals(listOf("move fix|acme/api#70 -1", "unqueue fix|acme/api#70", "enqueue fix|acme/api#71"), asked)
        assertEquals(listOf("unqueue fix|acme/api#70 bad=false", "enqueue fix|acme/api#71 bad=false"), said)
    }

    @Test
    fun enrollsAnEpic() {
        show()
        compose.onNodeWithTag("enroll").performClick()
        compose.onNodeWithTag("enroll-target").performTextReplacement("bad")
        compose.onNodeWithTag("enroll-go").performClick()
        compose.waitUntil(5_000) { asked.isNotEmpty() }
        compose.onNodeWithTag("enroll-error").assertTextEquals("not an issue")
        compose.onNodeWithTag("enroll-target").performTextReplacement("acme/docs#8")
        compose.onNodeWithTag("enroll-go").performClick()
        compose.waitUntil(5_000) { said.isNotEmpty() }
        assertEquals(listOf("Enrolled acme/docs#8 bad=false"), said)
    }

    @Test
    fun readOnlyWithoutDispatch() {
        show(dispatch = false)
        compose.onNodeWithTag("off").assertTextEquals("Dispatch is off on the hub")
        compose.onNodeWithTag("enroll").assertIsNotEnabled()
        compose.onNodeWithTag("approve-case:shared_failure|acme/widgets|lint").assertIsNotEnabled()
    }
}
