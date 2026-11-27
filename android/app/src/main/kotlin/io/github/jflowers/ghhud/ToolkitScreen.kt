package io.github.jflowers.ghhud

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.jflowers.ghhud.api.v1.ToolkitAction
import io.github.jflowers.ghhud.api.v1.ToolkitRolloutState
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.RolloutRow
import io.github.jflowers.ghhud.core.TargetRow
import io.github.jflowers.ghhud.core.ToolRow
import io.github.jflowers.ghhud.core.ToolkitCommands
import io.github.jflowers.ghhud.core.DriftRow
import io.github.jflowers.ghhud.core.ToolkitList
import io.github.jflowers.ghhud.core.Tone
import kotlinx.coroutines.launch

/** What the Toolkit screen can ask the hub for. Each answer is what to say. */
interface ToolkitActions {
    suspend fun checkTools(): Answer<String>

    suspend fun checkDrift(): Answer<String>

    suspend fun fixDrift(target: String): Answer<String>

    suspend fun rollOut(tool: String, version: String): Answer<String>

    suspend fun decline(id: String): Answer<String>

    suspend fun toolkit(action: ToolkitAction, r: RolloutRow): Answer<String>

    fun open(url: String)
}

/**
 * The TUI's T view: the tools the fleet pins (◆ when a release waits for
 * your go-ahead; a tap shows its notes, with Approve and Decline), then
 * the rollouts, each opening to its repos and controls.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ToolkitScreen(ui: Ui, act: ToolkitActions, say: Say, refresh: () -> Unit, open: String? = null) {
    val s = ui.hub.snapshot
    val off = ToolkitCommands.off(ui.hub)
    val tools = remember(s) { ToolkitList.tools(s) }
    val rollouts = remember(s) { ToolkitList.rollouts(s) }
    val drift = remember(s) { ToolkitList.drift(s) }
    val driftState = remember(s) { ToolkitList.driftState(s) }
    val scope = rememberCoroutineScope()
    var busy by remember { mutableStateOf(false) }
    // The rollout shown open: the one asked for, else the newest.
    var opened by rememberSaveable(open) { mutableStateOf(open ?: rollouts.firstOrNull()?.id) }
    var release by rememberSaveable { mutableStateOf<String?>(null) } // a tool
    var confirm by remember { mutableStateOf<ToolRow?>(null) }
    var fixing by remember { mutableStateOf<DriftRow?>(null) }
    var driftOpen by rememberSaveable { mutableStateOf<String?>(null) }
    fun run(f: suspend () -> Answer<String>) {
        busy = true
        scope.launch {
            when (val r = f()) {
                is Answer.Ok -> say(r.value, false)
                is Answer.No -> say(r.why, true)
            }
            busy = false
        }
    }
    Scaffold(
        contentWindowInsets = WindowInsets(0),
        topBar = {
            TopAppBar(
                title = { Text("Tools") },
                actions = {
                    TextButton(
                        onClick = { run { act.checkTools() } },
                        enabled = off == null && !busy && !s.toolkit.checking,
                        modifier = Modifier.testTag("check-tools"),
                    ) { Text("Check now") }
                    TextButton(
                        onClick = { run { act.checkDrift() } },
                        enabled = off == null && !busy && !s.toolkit.driftChecking,
                        modifier = Modifier.testTag("check-drift"),
                    ) { Text("Drift") }
                },
            )
        },
    ) { inner ->
        PullToRefreshBox(isRefreshing = ui.refreshing, onRefresh = refresh, modifier = Modifier.padding(inner).fillMaxSize()) {
            LazyColumn(Modifier.fillMaxSize().testTag("toolkit")) {
                item {
                    val state = ToolkitList.state(s)
                    Text(
                        state,
                        color = if (s.toolkit.error.isEmpty()) Tone.MUTED.color() else Tone.BAD.color(),
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp).testTag("toolkit-state"),
                    )
                    off?.let { Text(it, color = Tone.MUTED.color(), modifier = Modifier.padding(horizontal = 16.dp)) }
                }
                items(tools, key = { "tool:" + it.name }) { t ->
                    ToolLine(t, onTap = {
                        when {
                            t.proposal != null -> release = t.name
                            t.canRollOut && off == null -> confirm = t
                        }
                    })
                    HorizontalDivider()
                }
                if (drift.isNotEmpty() || driftState.isNotEmpty()) {
                    item {
                        Text(
                            "Scaffold drift",
                            style = MaterialTheme.typography.titleSmall,
                            color = MaterialTheme.colorScheme.primary,
                            modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 16.dp),
                        )
                        Text(
                            driftState,
                            style = MaterialTheme.typography.bodySmall,
                            color = if (s.toolkit.driftError.isEmpty()) Tone.MUTED.color() else Tone.BAD.color(),
                            modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp).testTag("drift-state"),
                        )
                    }
                    items(drift, key = { "drift:" + it.target }) { d ->
                        DriftLine(
                            d,
                            expanded = driftOpen == d.target,
                            enabled = off == null && !busy,
                            onTap = { driftOpen = if (driftOpen == d.target) null else d.target },
                            onFix = { fixing = d },
                            onOpen = act::open,
                        )
                        HorizontalDivider()
                    }
                }
                item {
                    Text(
                        "Rollouts",
                        style = MaterialTheme.typography.titleSmall,
                        color = MaterialTheme.colorScheme.primary,
                        modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 16.dp, bottom = 4.dp),
                    )
                    if (rollouts.isEmpty()) {
                        Text("No rollouts", color = Tone.MUTED.color(), modifier = Modifier.padding(16.dp))
                    }
                }
                items(rollouts, key = { "rollout:" + it.id }) { r ->
                    RolloutLine(
                        r,
                        expanded = r.id == opened,
                        enabled = off == null && !busy,
                        onTap = { opened = if (opened == r.id) null else r.id },
                        onControl = { a -> run { act.toolkit(a, r) } },
                        onOpen = act::open,
                    )
                    HorizontalDivider()
                }
            }
        }
    }
    release?.let { tool ->
        val p = ToolkitList.proposal(s, tool)
        ReleaseSheet(
            tool, p,
            enabled = off == null && !busy,
            onApprove = { r ->
                release = null
                run { act.rollOut(r.tool, r.version) }
            },
            onDecline = { r ->
                release = null
                run { act.decline(r.id) }
            },
            onOpen = act::open,
            onDismiss = { release = null },
        )
    }
    fixing?.let { d ->
        AlertDialog(
            onDismissRequest = { fixing = null },
            title = { Text("Fix ${d.target}?") },
            text = { Text("Re-scaffolds ${d.target} at the fleet's pins and opens a PR with what changes: ${d.files.size} file(s).") },
            confirmButton = {
                TextButton(onClick = {
                    fixing = null
                    run { act.fixDrift(d.target) }
                }, modifier = Modifier.testTag("fix-confirm")) { Text("Open scaffold PR") }
            },
            dismissButton = { TextButton(onClick = { fixing = null }) { Text("Cancel") } },
        )
    }
    confirm?.let { t ->
        AlertDialog(
            onDismissRequest = { confirm = null },
            title = { Text("Roll out ${t.name} ${t.latest}?") },
            text = {
                Text(
                    "Opens the dotfiles PR that pins ${t.name} at ${t.latest} (the fleet is on ${t.pin}), " +
                        "then re-scaffolds its repos, one PR each.",
                )
            },
            confirmButton = {
                TextButton(onClick = {
                    confirm = null
                    run { act.rollOut(t.name, t.latest) }
                }, modifier = Modifier.testTag("rollout-confirm")) { Text("Roll out") }
            },
            dismissButton = { TextButton(onClick = { confirm = null }) { Text("Cancel") } },
        )
    }
}

/** A tool: its pin and the latest release, ◆ when one waits for you. */
@Composable
private fun ToolLine(t: ToolRow, onTap: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().clickable(onClick = onTap).padding(horizontal = 16.dp, vertical = 10.dp).testTag("tool:" + t.name),
        verticalAlignment = Alignment.Top,
    ) {
        Text(t.mark.ifEmpty { " " }, color = t.tone.color(), modifier = Modifier.width(20.dp).testTag("tool-mark:" + t.name))
        Column(Modifier.weight(1f)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(t.name, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.weight(1f))
                Text(t.pin, fontFamily = FontFamily.Monospace)
                if (t.latest != t.pin && t.latest != "—") {
                    Text(" → ", color = Tone.MUTED.color())
                    Text(t.latest, fontFamily = FontFamily.Monospace, color = t.tone.color())
                }
            }
            val facts = listOfNotNull(
                t.repos.ifEmpty { null },
                t.rolling?.let { "rolling out $it" },
                if (t.proposal != null) "release waiting: tap for notes" else null,
            ).joinToString(" · ")
            if (facts.isNotEmpty()) Text(facts, style = MaterialTheme.typography.bodySmall, color = Tone.MUTED.color())
            if (t.error.isNotEmpty()) Text(t.error, style = MaterialTheme.typography.bodySmall, color = Tone.BAD.color())
        }
    }
}

/** A repo's scaffold against the pins; open, its files and Fix. */
@Composable
private fun DriftLine(
    d: DriftRow,
    expanded: Boolean,
    enabled: Boolean,
    onTap: () -> Unit,
    onFix: () -> Unit,
    onOpen: (String) -> Unit,
) {
    Column(Modifier.fillMaxWidth()) {
        Row(
            Modifier.fillMaxWidth().clickable(onClick = onTap).padding(horizontal = 16.dp, vertical = 10.dp).testTag("drift:" + d.target),
            verticalAlignment = Alignment.Top,
        ) {
            Text("●", color = d.tone.color(), modifier = Modifier.width(20.dp))
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(d.target, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.weight(1f),
                        maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(d.state, color = d.tone.color(), modifier = Modifier.testTag("drift-state:" + d.target))
                }
                if (d.note.isNotEmpty()) {
                    Text(d.note, style = MaterialTheme.typography.bodySmall,
                        color = if (d.tone == Tone.BAD) Tone.BAD.color() else Tone.MUTED.color(),
                        maxLines = if (expanded) 6 else 2, overflow = TextOverflow.Ellipsis)
                }
            }
        }
        if (!expanded) return@Column
        for (f in d.files) {
            Text(f, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
                modifier = Modifier.padding(start = 52.dp, end = 16.dp))
        }
        for (f in d.stale) {
            Text("$f (the init skips it)", style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace,
                color = Tone.MUTED.color(), modifier = Modifier.padding(start = 52.dp, end = 16.dp))
        }
        Row(Modifier.padding(start = 52.dp, end = 16.dp, top = 4.dp, bottom = 8.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            d.fix?.let { pr ->
                OutlinedButton(onClick = { onOpen(pr) }, modifier = Modifier.testTag("drift-pr:" + d.target)) { Text("Fix PR") }
            }
            if (d.fixable) {
                OutlinedButton(onClick = onFix, enabled = enabled, modifier = Modifier.testTag("fix:" + d.target)) { Text("Fix") }
            }
        }
    }
}

/** A rollout: stage and counts; open, its repos and its controls. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun RolloutLine(
    r: RolloutRow,
    expanded: Boolean,
    enabled: Boolean,
    onTap: () -> Unit,
    onControl: (ToolkitAction) -> Unit,
    onOpen: (String) -> Unit,
) {
    Column(Modifier.fillMaxWidth()) {
        Row(
            Modifier.fillMaxWidth().clickable(onClick = onTap).padding(horizontal = 16.dp, vertical = 10.dp).testTag("rollout:" + r.id),
            verticalAlignment = Alignment.Top,
        ) {
            Text(if (r.stage == "cancelled") "○" else "●", color = r.tone.color(), modifier = Modifier.width(20.dp))
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(r.id, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.weight(1f))
                    Text(r.stage, color = r.tone.color(), modifier = Modifier.testTag("rollout-stage:" + r.id))
                }
                Text(r.counts.ifEmpty { r.summary }, style = MaterialTheme.typography.bodySmall, color = Tone.MUTED.color(),
                    modifier = Modifier.testTag("rollout-counts:" + r.id))
                if (r.error.isNotEmpty()) Text(r.error, style = MaterialTheme.typography.bodySmall, color = Tone.BAD.color())
            }
        }
        if (!expanded) return@Column
        r.pinPr?.let { pr ->
            Text("Pin PR", color = MaterialTheme.colorScheme.primary,
                modifier = Modifier.clickable { onOpen(pr) }.padding(start = 52.dp, bottom = 4.dp))
        }
        for (t in r.targets) TargetLine(t, onOpen)
        FlowRow(
            Modifier.padding(start = 52.dp, end = 16.dp, top = 4.dp, bottom = 8.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            if (r.canCancel) {
                OutlinedButton(onClick = { onControl(ToolkitAction.TOOLKIT_ACTION_CANCEL) }, enabled = enabled,
                    modifier = Modifier.testTag("cancel:" + r.id)) { Text("Cancel") }
            }
            if (r.canRetry) {
                OutlinedButton(onClick = { onControl(ToolkitAction.TOOLKIT_ACTION_RETRY) }, enabled = enabled,
                    modifier = Modifier.testTag("retry:" + r.id)) { Text("Retry") }
            }
            if (r.canDismiss) {
                OutlinedButton(onClick = { onControl(ToolkitAction.TOOLKIT_ACTION_DISMISS) }, enabled = enabled,
                    modifier = Modifier.testTag("dismiss:" + r.id)) { Text("Dismiss") }
            }
        }
    }
}

/** A repo in a rollout: a tap opens its PR. */
@Composable
private fun TargetLine(t: TargetRow, onOpen: (String) -> Unit) {
    val tap = t.url?.let { u -> Modifier.clickable { onOpen(u) } } ?: Modifier
    Row(
        tap.fillMaxWidth().padding(start = 36.dp, end = 16.dp, top = 4.dp, bottom = 4.dp).testTag("target:" + t.target),
        verticalAlignment = Alignment.Top,
    ) {
        Text("●", color = t.tone.color(), modifier = Modifier.width(16.dp))
        Column(Modifier.weight(1f)) {
            Row {
                Text(t.target, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f),
                    maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(t.state, style = MaterialTheme.typography.bodyMedium, color = t.tone.color(),
                    modifier = Modifier.testTag("target-state:" + t.target))
            }
            val line = t.url?.let { "PR #" + it.substringAfterLast('/') } ?: t.note
            if (line.isNotEmpty()) {
                Text(line, style = MaterialTheme.typography.bodySmall,
                    color = if (t.url != null) MaterialTheme.colorScheme.primary else Tone.MUTED.color(),
                    maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

/** A waiting release's notes, with Approve and Decline. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun ReleaseSheet(
    tool: String,
    p: ToolkitRolloutState?,
    enabled: Boolean,
    onApprove: (ToolkitRolloutState) -> Unit,
    onDecline: (ToolkitRolloutState) -> Unit,
    onOpen: (String) -> Unit,
    onDismiss: () -> Unit,
) {
    ModalBottomSheet(onDismissRequest = onDismiss, sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)) {
        Column(Modifier.padding(horizontal = 20.dp).padding(bottom = 24.dp).testTag("release-sheet")) {
            if (p == null) {
                Text("$tool has no release waiting.", color = Tone.MUTED.color())
                return@Column
            }
            Text("${p.tool} ${p.version}", style = MaterialTheme.typography.titleLarge, fontWeight = FontWeight.SemiBold)
            Text(if (p.from.isEmpty()) "A new release." else "A new release; the fleet runs ${p.from}.", color = Tone.MUTED.color())
            Text(
                p.notes.ifEmpty { "No release notes." },
                style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.padding(vertical = 12.dp).heightIn(max = 360.dp).verticalScroll(rememberScrollState())
                    .testTag("release-notes"),
            )
            if (p.notesUrl.isNotEmpty()) {
                Text("Release page", color = MaterialTheme.colorScheme.primary,
                    modifier = Modifier.clickable { onOpen(p.notesUrl) }.padding(bottom = 12.dp))
            }
            Row {
                Spacer(Modifier.weight(1f))
                OutlinedButton(onClick = { onDecline(p) }, enabled = enabled, modifier = Modifier.testTag("release-decline")) {
                    Text("Decline")
                }
                Spacer(Modifier.width(8.dp))
                Button(onClick = { onApprove(p) }, enabled = enabled, modifier = Modifier.testTag("release-approve")) {
                    Text("Approve")
                }
            }
        }
    }
}
