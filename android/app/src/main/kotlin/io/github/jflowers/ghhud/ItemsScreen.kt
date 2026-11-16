package io.github.jflowers.ghhud

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Search
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import io.github.jflowers.ghhud.core.ItemCommands
import androidx.compose.material.icons.filled.Add
import androidx.compose.material3.InputChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import io.github.jflowers.ghhud.core.ColumnFilter
import io.github.jflowers.ghhud.core.ItemFilter
import io.github.jflowers.ghhud.core.ItemList
import io.github.jflowers.ghhud.core.ItemRow
import io.github.jflowers.ghhud.core.ItemSort
import io.github.jflowers.ghhud.core.Tone
import io.github.jflowers.ghhud.core.filterChoices
import java.time.Instant

/** How the item list is filtered, searched and sorted: kept by the model. */
data class ItemsView(
    val filter: ItemFilter = ItemFilter.ALL,
    val query: String = "",
    val kept: List<ColumnFilter> = emptyList(),
    val sort: ItemSort = ItemSort.HUB,
    val reversed: Boolean = false,
)

/** What the item list can ask for. */
interface ItemsControl {
    /**
     * Changes the view. The change applies to the view as it is then, not
     * as this screen last drew it: a text field can report after the view
     * has moved on.
     */
    fun update(change: (ItemsView) -> ItemsView)

    /** The search field's enter: its column terms become chips. */
    fun keepTerms()
    fun refresh()
}

/** The dashboard's items: a filter's rows, searched, narrowed and sorted. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ItemsScreen(
    ui: Ui,
    view: ItemsView,
    ctl: ItemsControl,
    onOpenItem: (ItemRow) -> Unit,
    now: Instant = Instant.now(),
    onNamed: (() -> Unit)? = null,
) {
    val s = ui.hub.snapshot
    val counts = remember(s) { ItemList.counts(s) }
    val rows = remember(s, view, now.epochSecond / 60) {
        view.sort.sort(ItemList.rows(s, view.filter, view.query, view.kept, now), view.reversed)
    }
    Scaffold(
        contentWindowInsets = WindowInsets(0),
        topBar = {
            TopAppBar(
                title = { Text(view.filter.title, modifier = Modifier.testTag("items-title")) },
                actions = {
                    // Start an agent on any issue or PR, by its link.
                    if (onNamed != null) {
                        IconButton(onClick = onNamed, enabled = ItemCommands.off(ui.hub) == null, modifier = Modifier.testTag("start-named")) {
                            Icon(Icons.Filled.Add, contentDescription = "Start an agent on a link")
                        }
                    }
                    SortMenu(view, ctl)
                },
            )
        },
    ) { inner ->
        Column(Modifier.padding(inner).fillMaxSize()) {
            LazyRow(
                horizontalArrangement = Arrangement.spacedBy(8.dp),
                modifier = Modifier.padding(horizontal = 12.dp),
            ) {
                items(ItemFilter.entries, key = { it.id }) { f ->
                    FilterChip(
                        selected = f == view.filter,
                        onClick = { ctl.update { it.copy(filter = f) } },
                        label = { Text("${f.shortTitle} ${counts[f] ?: 0}") },
                        modifier = Modifier.testTag("filter:" + f.id),
                    )
                }
            }
            ListSearch(
                view.query,
                view.kept,
                "Search, or repo:acme -author:bot",
                onQuery = { q -> ctl.update { it.copy(query = q) } },
                onKeep = { ctl.keepTerms() },
                onRemove = { f -> ctl.update { it.copy(kept = it.kept - f) } },
                onClear = { ctl.update { it.copy(kept = emptyList()) } },
            )
            PullToRefreshBox(isRefreshing = ui.refreshing, onRefresh = ctl::refresh, modifier = Modifier.fillMaxSize()) {
                LazyColumn(Modifier.fillMaxSize().testTag("items")) {
                    if (rows.isEmpty()) {
                        item {
                            Text(
                                if (view.query.isNotEmpty() || view.kept.isNotEmpty()) "Nothing matches." else "Nothing here.",
                                style = MaterialTheme.typography.bodyLarge,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                modifier = Modifier.padding(24.dp).testTag("items-empty"),
                            )
                        }
                    }
                    items(rows, key = { it.id }) { row ->
                        ItemRowView(row, now, onOpen = { onOpenItem(row) }, onFilter = { f ->
                            ctl.update { it.copy(kept = if (f in it.kept) it.kept else it.kept + f) }
                        })
                        HorizontalDivider()
                    }
                }
            }
        }
    }
}

@Composable
private fun SortMenu(view: ItemsView, ctl: ItemsControl) = SortMenu(
    ItemSort.entries, { it.label }, view.sort, view.reversed, ItemSort.HUB,
    onPick = { s -> ctl.update { it.copy(sort = s, reversed = false) } },
    onReverse = { ctl.update { it.copy(reversed = !it.reversed) } },
)

/**
 * A row: the title, then the TUI's cluster in small type. Tap opens it;
 * long press offers "Show only" and "Hide" for its values.
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun ItemRowView(row: ItemRow, now: Instant, onOpen: () -> Unit, onFilter: (ColumnFilter) -> Unit) {
    var menu by remember { mutableStateOf(false) }
    Box {
        Column(
            Modifier.fillMaxWidth()
                .combinedClickable(onClick = onOpen, onLongClick = { menu = true })
                .padding(horizontal = 16.dp, vertical = 10.dp)
                .testTag("item:" + row.id),
        ) {
            Row(verticalAlignment = Alignment.Top) {
                Text(
                    titleLine(row),
                    style = MaterialTheme.typography.bodyLarge,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f),
                )
                row.updated?.let {
                    Text(
                        ItemList.age(it, now),
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(start = 8.dp),
                    )
                }
            }
            Text(cluster(row, now), style = MaterialTheme.typography.bodySmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        FilterMenu(menu, row.filterChoices, onDismiss = { menu = false }, onFilter = onFilter)
    }
}

@Composable
private fun titleLine(row: ItemRow): AnnotatedString {
    val marker = row.marker?.color()
    return buildAnnotatedString {
        if (marker != null) withStyle(SpanStyle(color = marker)) { append("◆ ") }
        if (row.draft) withStyle(SpanStyle(color = Tone.MUTED.color())) { append("draft ") }
        append(row.title)
    }
}

/** "acme/r#1 · review · ci passing · approved · 2 threads · ann · fix ● 5m · ▸merge" */
@Composable
fun cluster(row: ItemRow, now: Instant): AnnotatedString {
    val muted = Tone.MUTED.color()
    val ci = row.ci?.color()
    val review = row.review?.color()
    val bad = Tone.BAD.color()
    val warn = Tone.WARN.color()
    val run = row.run?.tone?.color()
    val stage = row.stage?.tone?.color()
    return buildAnnotatedString {
        fun sep() = withStyle(SpanStyle(color = muted)) { append(" · ") }
        withStyle(SpanStyle(color = muted)) { append("${row.ref} · ${row.kind}") }
        val words = row.statusWords
        val ciWord = words.firstOrNull { it.startsWith("ci ") || it == "no checks" }
        if (ciWord != null && ci != null) { sep(); withStyle(SpanStyle(color = ci)) { append(ciWord) } }
        val reviewWord = words.firstOrNull { it in setOf("approved", "changes requested", "review required", "review pending") }
        if (reviewWord != null && review != null) { sep(); withStyle(SpanStyle(color = review)) { append(reviewWord) } }
        if (row.conflict) { sep(); withStyle(SpanStyle(color = bad)) { append("conflict") } }
        if (row.threads > 0 || row.botThreads > 0) {
            sep()
            if (row.threads > 0) withStyle(SpanStyle(color = warn)) { append("${row.threads} threads") }
            if (row.threads > 0 && row.botThreads > 0) append(" ")
            if (row.botThreads > 0) withStyle(SpanStyle(color = muted)) { append("${row.botThreads} bot") }
        }
        if (row.lastActor.isNotEmpty()) { sep(); withStyle(SpanStyle(color = muted)) { append(row.lastActor) } }
        if (row.run != null && run != null) { sep(); withStyle(SpanStyle(color = run)) { append(row.run!!.text(now)) } }
        if (row.stage != null && stage != null) { sep(); withStyle(SpanStyle(color = stage)) { append(row.stage!!.text) } }
    }
}

