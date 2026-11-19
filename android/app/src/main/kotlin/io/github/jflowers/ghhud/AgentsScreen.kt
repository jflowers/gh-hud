package io.github.jflowers.ghhud

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SecondaryTabRow
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import io.github.jflowers.ghhud.core.AgentList
import io.github.jflowers.ghhud.core.AgentRow
import io.github.jflowers.ghhud.core.AgentSort
import io.github.jflowers.ghhud.core.ColumnFilter
import io.github.jflowers.ghhud.core.ItemList
import io.github.jflowers.ghhud.core.TableFilter
import io.github.jflowers.ghhud.core.Tone
import io.github.jflowers.ghhud.core.WorkspaceList
import io.github.jflowers.ghhud.core.DeweyIndex
import io.github.jflowers.ghhud.core.WorkspaceRow
import io.github.jflowers.ghhud.core.WorkspaceSort
import io.github.jflowers.ghhud.core.filterChoices
import java.time.Instant

/** The Agents place's two lists. */
enum class AgentsTab { AGENTS, WORKSPACES }

/** How the Agents and Workspaces lists are shown: kept by the model. */
data class AgentsView(
    val tab: AgentsTab = AgentsTab.AGENTS,
    val liveOnly: Boolean = false,
    val showStale: Boolean = false,
    val agents: ListState<AgentSort> = ListState(AgentSort.HUB),
    val workspaces: ListState<WorkspaceSort> = ListState(WorkspaceSort.HUB),
)

/** What the Agents place can ask for. */
interface AgentsControl {
    /** Changes the view, as it is then (see ItemsControl.update). */
    fun updateAgents(change: (AgentsView) -> AgentsView)
    fun refreshAgents()
    fun refreshWorkspaces()
}

/**
 * Agent sessions and the runs behind them, and every checkout a dispatch
 * could use: the Mac's Agents and Workspaces panes, as two tabs.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AgentsScreen(
    ui: Ui,
    view: AgentsView,
    ctl: AgentsControl,
    onOpenAgent: (AgentRow) -> Unit,
    onOpenWorkspace: (WorkspaceRow) -> Unit,
    now: Instant = Instant.now(),
) {
    val s = ui.hub.snapshot
    Scaffold(
        contentWindowInsets = WindowInsets(0),
        topBar = {
            TopAppBar(
                title = { Text("Agents") },
                actions = {
                    if (view.tab == AgentsTab.AGENTS) {
                        SortMenu(
                            AgentSort.entries, { it.label }, view.agents.sort, view.agents.reversed, AgentSort.HUB,
                            onPick = { p -> ctl.updateAgents { it.copy(agents = it.agents.copy(sort = p, reversed = false)) } },
                            onReverse = { ctl.updateAgents { it.copy(agents = it.agents.copy(reversed = !it.agents.reversed)) } },
                        )
                    } else {
                        SortMenu(
                            WorkspaceSort.entries, { it.label }, view.workspaces.sort, view.workspaces.reversed, WorkspaceSort.HUB,
                            onPick = { p -> ctl.updateAgents { it.copy(workspaces = it.workspaces.copy(sort = p, reversed = false)) } },
                            onReverse = { ctl.updateAgents { it.copy(workspaces = it.workspaces.copy(reversed = !it.workspaces.reversed)) } },
                        )
                    }
                },
            )
        },
    ) { inner ->
        Column(Modifier.padding(inner).fillMaxSize()) {
            SecondaryTabRow(selectedTabIndex = view.tab.ordinal) {
                Tab(
                    selected = view.tab == AgentsTab.AGENTS,
                    onClick = { ctl.updateAgents { it.copy(tab = AgentsTab.AGENTS) } },
                    text = { Text("Agents · ${AgentList.running(s)} running") },
                    modifier = Modifier.testTag("tab:agents"),
                )
                Tab(
                    selected = view.tab == AgentsTab.WORKSPACES,
                    onClick = { ctl.updateAgents { it.copy(tab = AgentsTab.WORKSPACES) } },
                    text = { Text("Workspaces · ${WorkspaceList.ready(s)} ready") },
                    modifier = Modifier.testTag("tab:workspaces"),
                )
            }
            when (view.tab) {
                AgentsTab.AGENTS -> Agents(ui, view, ctl, onOpenAgent, now)
                AgentsTab.WORKSPACES -> Workspaces(ui, view, ctl, onOpenWorkspace, now)
            }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun Agents(ui: Ui, view: AgentsView, ctl: AgentsControl, onOpen: (AgentRow) -> Unit, now: Instant) {
    val s = ui.hub.snapshot
    val list = view.agents
    val rows = remember(s, view, now.epochSecond / 60) {
        val all = AgentList.rows(s, view.showStale, view.liveOnly, now)
        list.sort.sort(TableFilter.rows(all, AgentRow.COLUMNS, list.query, list.kept), list.reversed)
    }
    fun agents(change: (ListState<AgentSort>) -> ListState<AgentSort>) = ctl.updateAgents { it.copy(agents = change(it.agents)) }
    Row(Modifier.padding(horizontal = 12.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        FilterChip(view.liveOnly, onClick = { ctl.updateAgents { it.copy(liveOnly = !it.liveOnly) } },
            label = { Text("Live only") }, modifier = Modifier.testTag("live-only"))
        FilterChip(view.showStale, onClick = { ctl.updateAgents { it.copy(showStale = !it.showStale) } },
            label = { Text("Show stale") }, modifier = Modifier.testTag("show-stale"))
    }
    ListSearch(
        list.query, list.kept, "Search, or status:blocked",
        onQuery = { q -> agents { it.copy(query = q) } },
        onKeep = { agents { it.keepTerms(AgentRow.COLUMNS) } },
        onRemove = { f -> agents { it.copy(kept = it.kept - f) } },
        onClear = { agents { it.copy(kept = emptyList()) } },
    )
    PullToRefreshBox(isRefreshing = ui.refreshing, onRefresh = ctl::refreshAgents, modifier = Modifier.fillMaxSize()) {
        LazyColumn(Modifier.fillMaxSize().testTag("agents")) {
            if (rows.isEmpty()) {
                item {
                    Empty(
                        when {
                            list.query.isNotEmpty() || list.kept.isNotEmpty() -> "Nothing matches."
                            view.liveOnly -> "Nothing is running or blocked right now."
                            view.showStale -> "No agent sessions."
                            else -> "No agent sessions. Stale sessions are hidden."
                        },
                    )
                }
            }
            items(rows, key = { it.id }) { r ->
                AgentRowView(r, now, onOpen = { onOpen(r) }, onFilter = { f -> agents { it.adding(f) } })
                HorizontalDivider()
            }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun Workspaces(ui: Ui, view: AgentsView, ctl: AgentsControl, onOpen: (WorkspaceRow) -> Unit, now: Instant) {
    val s = ui.hub.snapshot
    val list = view.workspaces
    // Opening the list reads older checkouts again, as the TUI does.
    LaunchedEffect(Unit) { if (WorkspaceList.stale(s, now)) ctl.refreshWorkspaces() }
    val rows = remember(s, list) {
        list.sort.sort(TableFilter.rows(WorkspaceList.rows(s), WorkspaceRow.COLUMNS, list.query, list.kept), list.reversed)
    }
    fun workspaces(change: (ListState<WorkspaceSort>) -> ListState<WorkspaceSort>) =
        ctl.updateAgents { it.copy(workspaces = change(it.workspaces)) }
    ListSearch(
        list.query, list.kept, "Search, or where:chai",
        onQuery = { q -> workspaces { it.copy(query = q) } },
        onKeep = { workspaces { it.keepTerms(WorkspaceRow.COLUMNS) } },
        onRemove = { f -> workspaces { it.copy(kept = it.kept - f) } },
        onClear = { workspaces { it.copy(kept = emptyList()) } },
    )
    PullToRefreshBox(isRefreshing = ui.refreshing, onRefresh = ctl::refreshWorkspaces, modifier = Modifier.fillMaxSize()) {
        LazyColumn(Modifier.fillMaxSize().testTag("workspaces")) {
            if (rows.isEmpty()) {
                item {
                    Empty(
                        when {
                            list.query.isNotEmpty() || list.kept.isNotEmpty() -> "Nothing matches."
                            s.workspacesLoading -> "Reading the checkouts…"
                            else -> "No checkouts."
                        },
                    )
                }
            }
            items(rows, key = { it.id }) { r ->
                WorkspaceRowView(r, now, onOpen = { onOpen(r) }, onFilter = { f -> workspaces { it.adding(f) } })
                HorizontalDivider()
            }
        }
    }
}

@Composable
private fun Empty(text: String) {
    Text(text, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(24.dp).testTag("list-empty"))
}

/** "●" in the status's tone while it says something, "○" when idle. */
@Composable
private fun Glyph(tone: Tone, active: Boolean) {
    Text(if (active) "●" else "○", color = if (active) tone.color() else Tone.MUTED.color(), modifier = Modifier.padding(end = 10.dp))
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun AgentRowView(r: AgentRow, now: Instant, onOpen: () -> Unit, onFilter: (ColumnFilter) -> Unit) {
    var menu by remember { mutableStateOf(false) }
    Box {
        Row(
            Modifier.fillMaxWidth().combinedClickable(onClick = onOpen, onLongClick = { menu = true })
                .padding(horizontal = 16.dp, vertical = 10.dp).testTag("agent:" + r.id),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Glyph(r.tone, r.status.isNotEmpty())
            Column(Modifier.weight(1f)) {
                Text(
                    r.name,
                    style = MaterialTheme.typography.bodyLarge,
                    color = if (r.recent) Color.Unspecified else Tone.MUTED.color(),
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
                val where = r.machine + if (r.workspace.isEmpty()) "" else ":${r.workspace}"
                Text(
                    listOf(r.task.ifEmpty { "—" }, where).joinToString(" · "),
                    style = MaterialTheme.typography.bodySmall,
                    color = Tone.MUTED.color(),
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                val tail = listOfNotNull(
                    r.status.ifEmpty { null },
                    r.took?.let { "took " + AgentList.duration(it) },
                ).joinToString(" · ")
                if (tail.isNotEmpty()) Text(tail, style = MaterialTheme.typography.bodySmall, color = r.tone.color())
            }
            r.updated?.let {
                Text(ItemList.age(it, now), style = MaterialTheme.typography.bodySmall, color = Tone.MUTED.color(),
                    modifier = Modifier.padding(start = 8.dp))
            }
        }
        FilterMenu(menu, r.filterChoices.filter { it.first != "agent" }, onDismiss = { menu = false }, onFilter = onFilter)
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun WorkspaceRowView(r: WorkspaceRow, now: Instant, onOpen: () -> Unit, onFilter: (ColumnFilter) -> Unit) {
    var menu by remember { mutableStateOf(false) }
    Box {
        Row(
            Modifier.fillMaxWidth().combinedClickable(onClick = onOpen, onLongClick = { menu = true })
                .padding(horizontal = 16.dp, vertical = 10.dp).testTag("workspace:" + r.place),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Glyph(r.tone, true)
            Column(Modifier.weight(1f)) {
                Text(r.place, style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(
                    listOf(r.repo, r.branch.ifEmpty { "—" }, r.sync).joinToString(" · "),
                    style = MaterialTheme.typography.bodySmall,
                    color = Tone.MUTED.color(),
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                Text(r.state, style = MaterialTheme.typography.bodySmall, color = r.tone.color(), maxLines = 1, overflow = TextOverflow.Ellipsis)
                DeweyIndex.state(r.health, now).let { (text, tone) ->
                    Text("dewey $text", style = MaterialTheme.typography.bodySmall, color = indexColor(tone, DeweyIndex.machineProblem(r.health)),
                        maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.testTag("index:" + r.place))
                }
                if (r.notes.isNotEmpty()) Text(r.notes, style = MaterialTheme.typography.bodySmall, color = Tone.MUTED.color())
            }
        }
        FilterMenu(menu, r.filterChoices, onDismiss = { menu = false }, onFilter = onFilter)
    }
}


