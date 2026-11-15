package io.github.jflowers.ghhud

import androidx.compose.material3.MaterialTheme
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertTextContains
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import androidx.test.ext.junit.runners.AndroidJUnit4
import io.github.jflowers.ghhud.core.Approval
import io.github.jflowers.ghhud.core.Attention
import io.github.jflowers.ghhud.core.AttentionRow
import io.github.jflowers.ghhud.core.AttentionSection
import io.github.jflowers.ghhud.core.HubAddress
import io.github.jflowers.ghhud.core.HubState
import io.github.jflowers.ghhud.core.HubStatus
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

/** Runs on a device: the screen from a given state, and what taps ask for. */
@RunWith(AndroidJUnit4::class)
class AttentionScreenTest {
    @get:Rule
    val compose = createComposeRule()

    private val asked = mutableListOf<String>()
    private val act = object : AttentionActions {
        override fun open(url: String) { asked += "open $url" }
        override fun approve(row: AttentionRow) { asked += "approve ${row.id}" }
        override fun reject(row: AttentionRow) { asked += "reject ${row.id}" }
        override fun refresh() { asked += "refresh" }
        override fun setAddress(a: HubAddress) { asked += "address ${a.url} ${a.token}" }
    }

    private val proposal = AttentionRow(
        "proposal:p1", "Retry the flaky build", "fix pr · o/r", "https://github.com/o/r/issues/9", null,
        Approval.Proposal("p1"),
    )
    private val review = AttentionRow(
        "https://github.com/o/r/pull/1", "Add the thing", "o/r#1 · ann", "https://github.com/o/r/pull/1", null,
    )
    private val epic = AttentionRow(
        "epic:o/r#10", "Big thing", "o/r#10 · 2 pieces, 1 open", null, null, Approval.Epic("o/r#10", "plan1"),
    )
    private val attention = Attention(
        listOf(
            AttentionSection("proposals", "Proposals awaiting approval", listOf(proposal)),
            AttentionSection("epics", "Epic plans awaiting approval", listOf(epic)),
            AttentionSection("review", "Review requested", listOf(review)),
        ),
        3,
    )

    private fun show(ui: Ui) = compose.setContent { MaterialTheme { AttentionScreen(ui, act) } }

    @Test
    fun showsWhatNeedsYouAndSendsTaps() {
        show(Ui(HubAddress("https://hub"), HubState(HubStatus.Connected, attention = attention)))
        compose.onNodeWithTag("title").assertTextEquals("gh-hud · 3")
        compose.onNodeWithText("Review requested (1)").assertIsDisplayed()
        compose.onNodeWithText("Add the thing").performClick()
        compose.onNodeWithTag("approve:proposal:p1").performClick()
        compose.onNodeWithTag("reject:proposal:p1").performClick()
        assertEquals(
            listOf("open https://github.com/o/r/pull/1", "approve proposal:p1", "reject proposal:p1"),
            asked,
        )
    }

    @Test
    fun pendingAndRefused() {
        show(
            Ui(
                HubAddress("https://hub"),
                HubState(HubStatus.Connected, attention = attention),
                pending = setOf("epic:o/r#10"),
                errors = mapOf("proposal:p1" to "plan replaced"),
            ),
        )
        compose.onNodeWithText("Sent").assertIsDisplayed()
        compose.onNodeWithTag("approve:epic:o/r#10").assertDoesNotExist()
        // A refusal shows under the row, with the buttons back.
        compose.onNodeWithTag("error:proposal:p1", useUnmergedTree = true).assertTextEquals("plan replaced")
        compose.onNodeWithTag("approve:proposal:p1").assertIsDisplayed()
    }

    @Test
    fun lostAndEmpty() {
        show(Ui(HubAddress("https://hub"), HubState(HubStatus.Lost("connection refused"))))
        compose.onNodeWithTag("status").assertTextContains("Lost the hub: connection refused", substring = true)
        compose.onNodeWithTag("empty").assertDoesNotExist()
    }

    @Test
    fun asksForTheHub() {
        show(Ui(HubAddress(""), HubState(HubStatus.NotSet)))
        compose.onNodeWithText("Set the hub").performClick()
        compose.onNodeWithTag("url").performTextReplacement("https://gh-hud.example.ts.net")
        compose.onNodeWithTag("save").performClick()
        assertEquals(listOf("address https://gh-hud.example.ts.net "), asked)
    }
}
