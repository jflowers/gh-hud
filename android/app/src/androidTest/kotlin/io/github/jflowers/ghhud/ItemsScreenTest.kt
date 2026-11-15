package io.github.jflowers.ghhud

import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.assertTextContains
import androidx.compose.ui.test.assertTextEquals
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performImeAction
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.test.performTouchInput
import androidx.test.ext.junit.runners.AndroidJUnit4
import com.google.protobuf.timestamp
import io.github.jflowers.ghhud.api.v1.CIState
import io.github.jflowers.ghhud.api.v1.ItemKind
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.fetchResult
import io.github.jflowers.ghhud.api.v1.item
import io.github.jflowers.ghhud.api.v1.run as hubRun
import io.github.jflowers.ghhud.api.v1.snapshot
import io.github.jflowers.ghhud.core.AgentChoice
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.Attention
import io.github.jflowers.ghhud.core.Category
import io.github.jflowers.ghhud.core.ColumnFilter
import io.github.jflowers.ghhud.core.HubAddress
import io.github.jflowers.ghhud.core.HubState
import io.github.jflowers.ghhud.core.HubStatus
import io.github.jflowers.ghhud.core.ItemList
import io.github.jflowers.ghhud.core.ItemRow
import io.github.jflowers.ghhud.core.ItemSort
import io.github.jflowers.ghhud.core.keepingTerms
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import java.time.Instant

/** Runs on a device: the Items list and an item's screen, with a stand-in hub. */
@RunWith(AndroidJUnit4::class)
class ItemsScreenTest {
    @get:Rule
    val compose = createComposeRule()

    private val now = Instant.ofEpochSecond(1_000_000)

    private val snap: Snapshot = snapshot {
        dispatch = true
        result = fetchResult {
            user = "jay"
            items += item {
                kind = ItemKind.ITEM_KIND_PR; categories = Category.REVIEW_REQUESTED.bit
                url = "https://github.com/acme/r/pull/1"; repo = "acme/r"; num = 1; title = "Add the gizmo"
                author = "ann"; ci = CIState.CI_STATE_FAIL; review = "✅"; threads = 2
                updatedAt = timestamp { seconds = 1_000_000 - 300 }
            }
            items += item {
                kind = ItemKind.ITEM_KIND_PR; categories = Category.MINE.bit or Category.READY_FOR_MAINTAINER.bit
                url = "https://github.com/acme/r/pull/2"; repo = "acme/r"; num = 2; title = "Fix the build"
                author = "jay"; ci = CIState.CI_STATE_OK
                updatedAt = timestamp { seconds = 1_000_000 - 7200 }
            }
            items += item {
                kind = ItemKind.ITEM_KIND_ISSUE; categories = Category.ISSUES.bit
                url = "https://github.com/acme/s/issues/3"; repo = "acme/s"; num = 3; title = "Crash on start"
            }
        }
        runs += hubRun {
            id = "run-1"; task = "fix"; ref = "acme/r#2"; status = "failed"
            startedAt = timestamp { seconds = 1_000_000 - 4000 }; finishedAt = timestamp { seconds = 1_000_000 - 3600 }
        }
    }
    private val ui = Ui(HubAddress("https://hub"), HubState(HubStatus.Connected, snap, Attention.of(snap)))

    private val state = mutableStateOf(ItemsView())

    private fun list(): () -> ItemsView {
        val ctl = object : ItemsControl {
            override fun update(change: (ItemsView) -> ItemsView) { state.value = change(state.value) }
            override fun keepTerms() {
                val v = state.value
                val (kept, text) = v.kept.keepingTerms(v.query, ItemRow.COLUMNS)
                state.value = v.copy(kept = kept, query = text)
            }
            override fun refresh() {}
        }
        compose.setContent {
            MaterialTheme { ItemsScreen(ui, state.value, ctl, onOpenItem = { opened += it.id }, now = now) }
        }
        return { state.value }
    }

    private val opened = mutableListOf<String>()

    @Test
    fun filtersWithCounts() {
        list()
        compose.onNodeWithTag("filter:review").assertTextContains("Review 1", substring = true)
        compose.onNodeWithTag("filter:all").assertTextContains("All 3", substring = true)
        compose.onNodeWithTag("filter:mine").performClick()
        compose.onNodeWithTag("items-title").assertTextEquals("Mine")
        compose.onNodeWithText("Fix the build", substring = true).assertIsDisplayed()
        compose.onAllNodesWithTag("item:https://github.com/acme/r/pull/1").assertCountEquals(0)
    }

    @Test
    fun searchTermsBecomeChips() {
        val view = list()
        compose.onNodeWithTag("search").performTextReplacement("kind:issue crash")
        compose.onNodeWithTag("search").performImeAction()
        compose.waitForIdle()
        Thread.sleep(500) // the field reports its new text late; it must not undo the chip
        compose.waitForIdle()
        assertEquals(listOf(ColumnFilter.of("kind", "issue")), view().kept)
        assertEquals("crash", view().query)
        compose.onNodeWithTag("kept:kind:issue").assertIsDisplayed()
        compose.onNodeWithText("Crash on start", substring = true).assertIsDisplayed()
        compose.onNodeWithTag("kept:kind:issue").performClick() // removes it
        assertEquals(emptyList<ColumnFilter>(), view().kept)
    }

    @Test
    fun longPressFiltersAndSortReorders() {
        val view = list()
        compose.onNodeWithTag("item:https://github.com/acme/r/pull/1").performTouchInput { longClick() }
        compose.onNodeWithText("Hide author: ann").performClick()
        assertEquals(listOf(ColumnFilter.of("author", "ann", exclude = true)), view().kept)
        compose.onNodeWithTag("item:https://github.com/acme/r/pull/1").assertDoesNotExist()

        compose.onNodeWithTag("kept:-author:ann").performClick()
        compose.onNodeWithTag("sort").performClick()
        compose.onNodeWithTag("sort:NUMBER").performClick()
        assertEquals(ItemSort.NUMBER, view().sort)
        compose.onNodeWithTag("item:https://github.com/acme/r/pull/1").performClick()
        assertEquals(listOf("https://github.com/acme/r/pull/1"), opened)
    }

    private val asked = mutableListOf<String>()
    private val said = mutableListOf<String>()
    private val act = object : ItemActions {
        override fun open(url: String) { asked += "open $url" }
        override fun copy(text: String) { asked += "copy $text" }
        override suspend fun choices(row: ItemRow, queue: Boolean): Answer<List<AgentChoice>> {
            asked += "choices ${row.ref} queue=$queue"
            return Answer.Ok(listOf(AgentChoice("fix", "chai:r-2", "opencode run fix")))
        }
        override suspend fun run(row: ItemRow, c: AgentChoice, queue: Boolean): Answer<String> {
            asked += "run ${c.task} ${row.ref}"
            return Answer.Ok("Started ${c.task} on ${row.ref} in ${c.target}")
        }
        override suspend fun recheck(row: ItemRow): Answer<String> = Answer.No("Can't re-check fix on ${row.ref}: gone")
    }

    private fun item(id: String, off: String? = null) {
        val row = ItemList.rows(snap, io.github.jflowers.ghhud.core.ItemFilter.ALL, now = now).first { it.id == id }
        compose.setContent {
            MaterialTheme { ItemScreen(row, off, act, say = { t, bad -> said += "$t bad=$bad" }, onBack = {}, now = now) }
        }
    }

    @Test
    fun anItemStartsAnAgent() {
        item("https://github.com/acme/r/pull/1")
        compose.onNodeWithTag("fact:Status").assertTextEquals("ci failing, approved")
        compose.onNodeWithTag("fact:Threads").assertTextContains("2 from people", substring = true)
        compose.onNodeWithTag("recheck").assertDoesNotExist() // no run
        compose.onNodeWithTag("start").performClick()
        compose.onNodeWithTag("choice:fix|chai:r-2").performClick()
        compose.waitUntil(5_000) { said.isNotEmpty() }
        assertEquals(listOf("choices acme/r#1 queue=false", "run fix acme/r#1"), asked)
        assertEquals(listOf("Started fix on acme/r#1 in chai:r-2 bad=false"), said)
    }

    @Test
    fun aFinishedRunIsRechecked() {
        item("https://github.com/acme/r/pull/2")
        compose.onNodeWithTag("fact:Agent run").assertTextContains("fix ✗ 1h", substring = true)
        compose.onNodeWithTag("recheck").performClick()
        compose.waitUntil(5_000) { said.size == 2 }
        assertEquals("Can't re-check fix on acme/r#2: gone bad=true", said[1])
    }

    @Test
    fun commandsOffSayWhy() {
        item("https://github.com/acme/r/pull/1", off = "Read-only: gh-hud pid 7 owns dispatch")
        compose.onNodeWithTag("off").assertTextEquals("Read-only: gh-hud pid 7 owns dispatch")
        compose.onNodeWithTag("start").assertIsNotEnabled()
        compose.onNodeWithTag("open").performClick()
        assertEquals(listOf("open https://github.com/acme/r/pull/1"), asked)
    }
}
