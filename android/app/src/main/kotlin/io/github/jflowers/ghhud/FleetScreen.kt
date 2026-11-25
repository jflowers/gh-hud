package io.github.jflowers.ghhud

import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Checkbox
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.jflowers.ghhud.api.v1.FleetAction
import io.github.jflowers.ghhud.api.v1.FleetMachine
import io.github.jflowers.ghhud.api.v1.FleetSnapshot
import io.github.jflowers.ghhud.core.AgentList
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.ColumnFilter
import io.github.jflowers.ghhud.core.DeweyCommands
import io.github.jflowers.ghhud.core.DeweyFleet
import io.github.jflowers.ghhud.core.FleetCommands
import io.github.jflowers.ghhud.core.FleetList
import io.github.jflowers.ghhud.core.FleetRow
import io.github.jflowers.ghhud.core.FleetSort
import io.github.jflowers.ghhud.core.TableFilter
import io.github.jflowers.ghhud.core.Tone
import io.github.jflowers.ghhud.core.filterChoices
import kotlinx.coroutines.launch
import java.time.Instant

/** What the Fleet can ask the hub for. */
interface FleetActions {
    /** Runs a control; the answer is what to say. */
    suspend fun fleet(action: FleetAction): Answer<String>

    /**
     * Starts a Dewey reindex pass on a machine, or on every one (null):
     * the vaults that need it, or every vault when forced.
     */
    suspend fun dewey(machine: String?, force: Boolean): Answer<String>

    /** Cancels the Dewey reindex pass on a machine, or on every one (null). */
    suspend fun deweyCancel(machine: String?): Answer<String>

    /** Changes the machine list's view, as it is then (see ItemsControl.update). */
    fun updateFleet(change: (ListState<FleetSort>) -> ListState<FleetSort>)
}

/** A Dewey reindex pass waiting for your go-ahead: on a machine, or every one (null). */
private data class DeweyAsk(val machine: String?)

/**
 * The GitOps rollout: the target and its CI, the controls (the TUI's p, a,
 * s and r), Dewey reindex passes (d, D, f and x), and a row per machine.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun FleetScreen(ui: Ui, view: ListState<FleetSort>, act: FleetActions, say: Say, refresh: () -> Unit, now: Instant = Instant.now()) {
    val s = ui.hub.snapshot
    val f = s.fleet
    val off = FleetCommands.off(ui.hub)
    val rows = remember(s, view) {
        view.sort.sort(TableFilter.rows(FleetList.rows(s), FleetRow.COLUMNS, view.query, view.kept), view.reversed)
    }
    val scope = rememberCoroutineScope()
    var busy by remember { mutableStateOf(false) }
    var confirmSkip by remember { mutableStateOf(false) }
    var confirmDewey by remember { mutableStateOf<DeweyAsk?>(null) }
    fun answer(call: suspend () -> Answer<String>) {
        busy = true
        scope.launch {
            when (val r = call()) {
                is Answer.Ok -> say(r.value, false)
                is Answer.No -> say(r.why, true)
            }
            busy = false
        }
    }
    fun run(a: FleetAction) = answer { act.fleet(a) }
    val enabled = off == null && !busy
    Scaffold(
        contentWindowInsets = WindowInsets(0),
        topBar = {
            TopAppBar(
                title = { Text("Fleet") },
                actions = {
                    SortMenu(
                        FleetSort.entries, { it.label }, view.sort, view.reversed, FleetSort.HUB,
                        onPick = { p -> act.updateFleet { it.copy(sort = p, reversed = false) } },
                        onReverse = { act.updateFleet { it.copy(reversed = !it.reversed) } },
                    )
                },
            )
        },
    ) { inner ->
        PullToRefreshBox(isRefreshing = ui.refreshing, onRefresh = refresh, modifier = Modifier.padding(inner).fillMaxSize()) {
            LazyColumn(Modifier.fillMaxSize().testTag("fleet")) {
                item {
                    Header(f)
                    Controls(f, enabled = enabled, onRun = ::run, onSkip = { confirmSkip = true })
                    DeweyControls(
                        f,
                        enabled = enabled,
                        onStart = { confirmDewey = DeweyAsk(null) },
                        onCancel = { answer { act.deweyCancel(null) } },
                    )
                    off?.let {
                        Text(it, color = Tone.MUTED.color(), modifier = Modifier.padding(horizontal = 16.dp).testTag("off"))
                    }
                    HorizontalDivider(Modifier.padding(top = 8.dp))
                    ListSearch(
                        view.query, view.kept, "Search, or state:failed",
                        onQuery = { q -> act.updateFleet { it.copy(query = q) } },
                        onKeep = { act.updateFleet { it.keepTerms(FleetRow.COLUMNS) } },
                        onRemove = { k -> act.updateFleet { it.copy(kept = it.kept - k) } },
                        onClear = { act.updateFleet { it.copy(kept = emptyList()) } },
                    )
                }
                if (rows.isEmpty() && (view.query.isNotEmpty() || view.kept.isNotEmpty())) {
                    item {
                        Text("Nothing matches.", color = Tone.MUTED.color(), modifier = Modifier.padding(24.dp).testTag("list-empty"))
                    }
                }
                items(rows, key = { it.name }) { r ->
                    val m = f.machinesList.firstOrNull { it.name == r.name }
                    MachineRow(
                        r, m, now, enabled,
                        onFilter = { k -> act.updateFleet { it.adding(k) } },
                        onReindex = { confirmDewey = DeweyAsk(r.name) },
                        onCancel = { answer { act.deweyCancel(r.name) } },
                    )
                    HorizontalDivider()
                }
            }
        }
    }
    if (confirmSkip) {
        AlertDialog(
            onDismissRequest = { confirmSkip = false },
            title = { Text("Skip the drain wait?") },
            text = {
                Text(
                    "The machine being updated applies the new commit now, without waiting for its running agents " +
                        "to finish. They may be interrupted.",
                )
            },
            confirmButton = {
                TextButton(onClick = {
                    confirmSkip = false
                    run(FleetAction.FLEET_ACTION_SKIP_WAIT)
                }, modifier = Modifier.testTag("skip-confirm")) { Text("Skip wait", color = MaterialTheme.colorScheme.error) }
            },
            dismissButton = { TextButton(onClick = { confirmSkip = false }) { Text("Cancel") } },
        )
    }
    confirmDewey?.let { ask ->
        DeweyDialog(ask.machine, onDismiss = { confirmDewey = null }, onStart = { force ->
            confirmDewey = null
            answer { act.dewey(ask.machine, force) }
        })
    }
}

/** The go-ahead for a Dewey reindex pass, with Force: every vault, not just the ones that need it. */
@Composable
private fun DeweyDialog(machine: String?, onDismiss: () -> Unit, onStart: (force: Boolean) -> Unit) {
    var force by remember { mutableStateOf(false) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(DeweyCommands.ask(machine, force), modifier = Modifier.testTag("dewey-ask")) },
        text = {
            Column {
                Text(DeweyCommands.explain(force))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Checkbox(checked = force, onCheckedChange = { force = it }, modifier = Modifier.testTag("dewey-force"))
                    Text("Force: every vault, whatever its check says")
                }
            }
        },
        confirmButton = {
            TextButton(onClick = { onStart(force) }, modifier = Modifier.testTag("dewey-confirm")) { Text("Reindex") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}

/** Reindex the Dewey vaults on every machine, or cancel the passes that run. A machine's own are on its row. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun DeweyControls(f: FleetSnapshot, enabled: Boolean, onStart: () -> Unit, onCancel: () -> Unit) {
    FlowRow(
        Modifier.padding(horizontal = 16.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        OutlinedButton(onClick = onStart, enabled = enabled && f.machinesCount > 0, modifier = Modifier.testTag("dewey-all")) {
            Text("Reindex vaults now…")
        }
        if (DeweyCommands.running(f)) {
            OutlinedButton(onClick = onCancel, enabled = enabled, modifier = Modifier.testTag("dewey-cancel-all")) {
                Text("Cancel passes")
            }
        }
    }
}

@Composable
private fun Header(f: FleetSnapshot) {
    val (state, tone) = FleetList.state(f)
    Column(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
        Text(state, style = MaterialTheme.typography.titleLarge, fontWeight = FontWeight.SemiBold, color = tone.color(),
            modifier = Modifier.testTag("fleet-state"))
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (f.targetShort.isEmpty()) {
                Text("no target yet", color = Tone.MUTED.color())
            } else {
                Text("target ", color = Tone.MUTED.color())
                Text(f.targetShort, fontFamily = FontFamily.Monospace)
            }
            Text("   CI ", color = Tone.MUTED.color())
            Text(f.ci.ifEmpty { "unknown" }, color = FleetList.ciTone(f.ci).color(), modifier = Modifier.testTag("fleet-ci"))
        }
        if (f.ciDetail.isNotEmpty()) Text(f.ciDetail, style = MaterialTheme.typography.bodySmall, color = Tone.MUTED.color())
        if (f.banner.isNotEmpty()) Text("⚠ ${f.banner}", color = Tone.WARN.color(), modifier = Modifier.padding(top = 4.dp))
        if (f.lastError.isNotEmpty()) Text("✖ ${f.lastError}", color = Tone.BAD.color(), modifier = Modifier.padding(top = 4.dp))
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun Controls(f: FleetSnapshot, enabled: Boolean, onRun: (FleetAction) -> Unit, onSkip: () -> Unit) {
    FlowRow(
        Modifier.padding(horizontal = 16.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        OutlinedButton(
            onClick = { onRun(if (f.paused) FleetAction.FLEET_ACTION_RESUME else FleetAction.FLEET_ACTION_PAUSE) },
            enabled = enabled,
            modifier = Modifier.testTag("pause"),
        ) { Text(if (f.paused) "Resume" else "Pause") }
        OutlinedButton(onClick = { onRun(FleetAction.FLEET_ACTION_APPLY_NOW) }, enabled = enabled, modifier = Modifier.testTag("apply-now")) {
            Text("Check now")
        }
        OutlinedButton(onClick = onSkip, enabled = enabled && FleetCommands.canSkipWait(f), modifier = Modifier.testTag("skip-wait")) {
            Text("Skip wait")
        }
        OutlinedButton(
            onClick = { onRun(FleetAction.FLEET_ACTION_RETRY) },
            enabled = enabled && FleetCommands.canRetry(f),
            modifier = Modifier.testTag("retry"),
        ) { Text(if (f.failed.isEmpty()) "Retry" else "Retry ${f.failed}") }
    }
}

/**
 * A machine: its state, the commit it has (and the target, while it
 * isn't there), slots, how long it has drained, its last error, and its
 * Dewey reindexes. A tap shows the whole error and the machine's Dewey
 * controls; a long press filters by its values.
 */
@OptIn(ExperimentalFoundationApi::class, ExperimentalLayoutApi::class)
@Composable
private fun MachineRow(
    r: FleetRow,
    m: FleetMachine?,
    now: Instant,
    enabled: Boolean,
    onFilter: (ColumnFilter) -> Unit,
    onReindex: () -> Unit,
    onCancel: () -> Unit,
) {
    var menu by remember { mutableStateOf(false) }
    var whole by remember { mutableStateOf(false) }
    Box {
        Row(
            Modifier.fillMaxWidth().combinedClickable(onClick = { whole = !whole }, onLongClick = { menu = true })
                .padding(horizontal = 16.dp, vertical = 10.dp).testTag("machine:" + r.name),
            verticalAlignment = Alignment.Top,
        ) {
            Text(if (r.state == "pending") "○" else "●", color = if (r.state == "pending") Tone.MUTED.color() else r.tone.color(),
                modifier = Modifier.padding(end = 10.dp))
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(r.name, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.weight(1f))
                    Text(r.stateText, color = r.tone.color(), modifier = Modifier.testTag("machine-state:" + r.name))
                }
                Text(r.applied, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace, maxLines = 1,
                    overflow = TextOverflow.Ellipsis)
                val facts = listOfNotNull(
                    "reached " + r.reach,
                    if (r.slots == "—") null else "slots " + r.slots,
                    r.waited?.let { "drained " + AgentList.duration(it) },
                ).joinToString(" · ")
                Text(facts, style = MaterialTheme.typography.bodySmall, color = if (r.reach == "no") Tone.WARN.color() else Tone.MUTED.color())
                if (r.error.isNotEmpty()) {
                    Text(r.error, style = MaterialTheme.typography.bodySmall, color = Tone.BAD.color(),
                        maxLines = if (whole) Int.MAX_VALUE else 2, overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.testTag("machine-error:" + r.name))
                }
                if (r.pruned.isNotEmpty()) {
                    Text(r.pruned, style = MaterialTheme.typography.bodySmall, color = r.prunedTone.color(),
                        maxLines = if (whole) Int.MAX_VALUE else 1, overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.testTag("machine-pruned:" + r.name))
                }
                if (m != null) {
                    for ((i, l) in (DeweyFleet.rolloutLines(m) + DeweyFleet.passLines(m, now)).withIndex()) {
                        Text(l.text, style = MaterialTheme.typography.bodySmall, color = l.tone.color(),
                            maxLines = if (whole) Int.MAX_VALUE else 1, overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.testTag("dewey-line:${r.name}:$i"))
                    }
                    if (whole) {
                        FlowRow(Modifier.padding(top = 4.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            OutlinedButton(onClick = onReindex, enabled = enabled, modifier = Modifier.testTag("dewey:" + r.name)) {
                                Text("Reindex vaults now…")
                            }
                            if (m.hasDeweyPass() && m.deweyPass.running) {
                                OutlinedButton(onClick = onCancel, enabled = enabled, modifier = Modifier.testTag("dewey-cancel:" + r.name)) {
                                    Text("Cancel pass")
                                }
                            }
                        }
                    }
                }
            }
        }
        FilterMenu(menu, r.filterChoices, onDismiss = { menu = false }, onFilter = onFilter)
    }
}
