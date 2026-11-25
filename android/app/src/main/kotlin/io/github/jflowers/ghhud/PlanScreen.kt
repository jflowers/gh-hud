package io.github.jflowers.ghhud

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
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
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.CaseRow
import io.github.jflowers.ghhud.core.EpicRow
import io.github.jflowers.ghhud.core.ItemCommands
import io.github.jflowers.ghhud.core.ItemRow
import io.github.jflowers.ghhud.core.PlanItem
import io.github.jflowers.ghhud.core.PlanList
import io.github.jflowers.ghhud.core.Tone
import kotlinx.coroutines.launch

/** What the Plan can ask the hub for. Answers say how it went; "" says nothing. */
interface PlanActions {
    suspend fun enqueue(r: PlanItem): Answer<String>
    suspend fun move(r: PlanItem, by: Long): Answer<String>
    suspend fun unqueue(r: PlanItem): Answer<String>
    suspend fun approve(c: CaseRow): Answer<String>
    suspend fun reject(c: CaseRow): Answer<String>
    suspend fun approve(e: EpicRow): Answer<String>
    suspend fun reread(e: EpicRow): Answer<String>
    suspend fun drop(e: EpicRow): Answer<String>
    suspend fun finish(e: EpicRow): Answer<String>
    suspend fun clearGate(epic: String, piece: String): Answer<String>
    suspend fun enroll(target: String): Answer<String>
}

/**
 * The Plan: open cases and enrolled epics, then what agents work on next
 * in start order, and what's running, waiting, stopped and merging.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun PlanScreen(ui: Ui, plan: PlanActions, items: ItemActions, say: Say, refresh: () -> Unit) {
    val s = ui.hub.snapshot
    val off = ItemCommands.off(ui.hub)
    val cases = remember(s) { PlanList.cases(s) }
    val epics = remember(s) { PlanList.epics(s) }
    val groups = remember(s) { PlanList.groups(s) }
    val scope = rememberCoroutineScope()
    var busy by remember { mutableStateOf(setOf<String>()) }
    var enrolling by remember { mutableStateOf(false) }
    var dropping by remember { mutableStateOf<EpicRow?>(null) }
    var finishing by remember { mutableStateOf<EpicRow?>(null) }
    var sheet by remember { mutableStateOf<Pair<ItemRow, Boolean>?>(null) }

    /** Runs a command once at a time per key, and says how it went. */
    fun go(key: String, cmd: suspend () -> Answer<String>) {
        if (key in busy) return
        busy = busy + key
        scope.launch {
            when (val a = cmd()) {
                is Answer.Ok -> if (a.value.isNotEmpty()) say(a.value, false)
                is Answer.No -> say(a.why, true)
            }
            busy = busy - key
        }
    }

    Scaffold(
        contentWindowInsets = WindowInsets(0),
        topBar = {
            TopAppBar(
                title = { Text("Plan") },
                actions = {
                    TextButton(onClick = { enrolling = true }, enabled = off == null, modifier = Modifier.testTag("enroll")) {
                        Text("Enroll epic")
                    }
                },
            )
        },
    ) { inner ->
        PullToRefreshBox(isRefreshing = ui.refreshing, onRefresh = refresh, modifier = Modifier.padding(inner).fillMaxSize()) {
            LazyColumn(Modifier.fillMaxSize().testTag("plan")) {
                item {
                    Text(
                        PlanList.summary(s),
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp).testTag("plan-summary"),
                    )
                    off?.let {
                        Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant,
                            modifier = Modifier.padding(horizontal = 16.dp).testTag("off"))
                    }
                }
                if (cases.isNotEmpty()) {
                    item { Heading("Cases") }
                    items(cases, key = { "case:" + it.id }) { c ->
                        CaseView(c, off == null && "case:${c.id}" !in busy, onApprove = { go("case:${c.id}") { plan.approve(c) } },
                            onReject = { go("case:${c.id}") { plan.reject(c) } })
                        HorizontalDivider()
                    }
                }
                if (epics.isNotEmpty()) {
                    item { Heading("Epics") }
                    items(epics, key = { "epic:" + it.ref }) { e ->
                        EpicView(
                            e,
                            enabled = off == null && "epic:${e.ref}" !in busy,
                            onOpen = { e.url?.let(items::open) },
                            onApprove = { go("epic:${e.ref}") { plan.approve(e) } },
                            onReread = { go("epic:${e.ref}") { plan.reread(e) } },
                            onDrop = { dropping = e },
                            onFinish = { finishing = e },
                            onClear = { p -> go("gate:${e.ref} $p") { plan.clearGate(e.ref, p) } },
                        )
                        HorizontalDivider()
                    }
                }
                for (g in groups) {
                    item(key = "group:" + g.title) { Heading(g.title) }
                    items(g.rows, key = { "row:" + it.id }) { r ->
                        PlanRowView(
                            r,
                            off = off,
                            busy = "row:${r.id}" in busy,
                            onMove = { by -> go("row:${r.id}") { plan.move(r, by) } },
                            onUnqueue = { go("row:${r.id}") { plan.unqueue(r) } },
                            onEnqueue = { go("row:${r.id}") { plan.enqueue(r) } },
                            onClear = { go("row:${r.id}") { plan.clearGate(r.epic, r.ref) } },
                            onOpen = { r.url?.let(items::open) },
                            onCopy = { r.url?.let(items::copy) },
                            onAgent = { queue -> sheet = ItemRow.of(r.item, s.result.user) to queue },
                        )
                        HorizontalDivider()
                    }
                }
                if (groups.isEmpty()) {
                    item {
                        Text(
                            "Nothing queued, due, running or waiting.",
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            modifier = Modifier.padding(16.dp).testTag("plan-empty"),
                        )
                    }
                }
            }
        }
    }
    if (enrolling) {
        EnrollDialog(plan, onDone = { said ->
            enrolling = false
            said?.let { say(it, false) }
        })
    }
    dropping?.let { e ->
        AlertDialog(
            onDismissRequest = { dropping = null },
            title = { Text("Stop following ${e.ref}?") },
            text = {
                Text(
                    "gh-hud stops reading ${e.ref} and starts none of its pieces. Pieces already running carry on; " +
                        "you can enroll it again later.",
                )
            },
            confirmButton = {
                TextButton(onClick = {
                    dropping = null
                    go("epic:${e.ref}") { plan.drop(e) }
                }, modifier = Modifier.testTag("drop-confirm")) { Text("Drop epic", color = MaterialTheme.colorScheme.error) }
            },
            dismissButton = { TextButton(onClick = { dropping = null }) { Text("Cancel") } },
        )
    }
    finishing?.let { e ->
        AlertDialog(
            onDismissRequest = { finishing = null },
            title = { Text("Finish ${e.ref}?") },
            text = { Text("gh-hud posts the summary shown under ${e.ref} as you, then closes the epic as completed.") },
            confirmButton = {
                TextButton(onClick = {
                    finishing = null
                    go("epic:${e.ref}") { plan.finish(e) }
                }, modifier = Modifier.testTag("finish-confirm")) { Text("Post summary and close") }
            },
            dismissButton = { TextButton(onClick = { finishing = null }) { Text("Cancel") } },
        )
    }
    sheet?.let { (row, queue) ->
        AgentSheet(row, queue, items, onDone = { said ->
            sheet = null
            said?.let { say(it, false) }
        })
    }
}

@Composable
private fun Heading(title: String) {
    Text(
        title,
        style = MaterialTheme.typography.titleSmall,
        color = MaterialTheme.colorScheme.primary,
        modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 16.dp, bottom = 4.dp),
    )
}

@Composable
private fun CaseView(c: CaseRow, enabled: Boolean, onApprove: () -> Unit, onReject: () -> Unit) {
    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 10.dp).testTag("case:" + c.id)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("⚑ ", color = Tone.WARN.color())
            Text(c.title, fontWeight = FontWeight.Medium, modifier = Modifier.weight(1f, fill = false))
            Text("  " + c.scope, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
        Text(c.status, style = MaterialTheme.typography.bodySmall, color = c.tone.color())
        if (c.proposal != null) {
            if (c.summary.isNotEmpty()) {
                Text(c.summary, style = MaterialTheme.typography.bodyMedium, maxLines = 3, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.padding(top = 4.dp))
            }
            Row(Modifier.padding(top = 6.dp), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(onClick = onApprove, enabled = enabled, modifier = Modifier.testTag("approve-case:" + c.id)) { Text("Approve") }
                OutlinedButton(onClick = onReject, enabled = enabled, modifier = Modifier.testTag("reject-case:" + c.id)) { Text("Reject") }
            }
        }
    }
}

/** Open pieces listed before "and N more"; tapping that shows them all. */
private const val SHOWN = 8

@Composable
private fun EpicView(
    e: EpicRow,
    enabled: Boolean,
    onOpen: () -> Unit,
    onApprove: () -> Unit,
    onReread: () -> Unit,
    onDrop: () -> Unit,
    onFinish: () -> Unit,
    onClear: (piece: String) -> Unit,
) {
    var menu by remember { mutableStateOf(false) }
    var all by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth().padding(start = 16.dp, top = 6.dp, bottom = 10.dp).testTag("epic:" + e.ref)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Text(e.title.ifEmpty { e.ref }, fontWeight = FontWeight.Medium, maxLines = 2, overflow = TextOverflow.Ellipsis)
                Text(e.ref, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
            Box {
                IconButton(onClick = { menu = true }, enabled = enabled, modifier = Modifier.testTag("epic-menu:" + e.ref)) {
                    Icon(Icons.Filled.MoreVert, contentDescription = "More for ${e.ref}")
                }
                DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                    if (e.url != null) DropdownMenuItem(text = { Text("Open on GitHub") }, onClick = { menu = false; onOpen() })
                    DropdownMenuItem(text = { Text("Reread") }, onClick = { menu = false; onReread() })
                    HorizontalDivider()
                    DropdownMenuItem(
                        text = { Text("Drop…", color = MaterialTheme.colorScheme.error) },
                        onClick = { menu = false; onDrop() },
                        modifier = Modifier.testTag("drop:" + e.ref),
                    )
                }
            }
        }
        Text(e.status, style = MaterialTheme.typography.bodySmall, color = e.tone.color(), modifier = Modifier.padding(end = 16.dp))
        if (e.changes.isNotEmpty()) {
            Column(Modifier.padding(top = 4.dp, end = 16.dp).testTag("changes:" + e.ref)) {
                Text(
                    "Changed since you approved it:",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                for (c in e.changes) {
                    Text("± $c", style = MaterialTheme.typography.bodySmall, color = Tone.WARN.color())
                }
            }
        }
        if (e.planToApprove != null) {
            Button(onClick = onApprove, enabled = enabled, modifier = Modifier.padding(top = 6.dp).testTag("approve-epic:" + e.ref)) {
                Text("Approve plan")
            }
        }
        if (e.planToFinish != null) {
            Column(Modifier.padding(top = 4.dp, end = 16.dp).testTag("summary:" + e.ref)) {
                Text(
                    "Closes with this comment, posted as you:",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Text(e.summary.trim(), style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace)
            }
            Button(onClick = onFinish, enabled = enabled, modifier = Modifier.padding(top = 6.dp).testTag("finish-epic:" + e.ref)) {
                Text("Finish epic…")
            }
        }
        val pieces = if (all) e.open else e.open.take(SHOWN)
        for (p in pieces) {
            Row(
                Modifier.padding(start = 8.dp, end = 16.dp, top = 2.dp).alpha(if (p.active) 1f else 0.65f),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(EpicRow.mark(p.state), modifier = Modifier.width(18.dp))
                Column(Modifier.weight(1f)) {
                    Text("${p.short}  ${p.title}", style = MaterialTheme.typography.bodyMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    if (p.note.isNotEmpty()) {
                        Text(p.note, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant, maxLines = 1)
                    }
                }
                if (p.clearable) {
                    OutlinedButton(onClick = { onClear(p.ref) }, enabled = enabled, modifier = Modifier.testTag("clear:" + p.ref)) {
                        Text("Clear gate")
                    }
                }
            }
        }
        if (!all && e.open.size > SHOWN) {
            Text(
                "and ${e.open.size - SHOWN} more",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.primary,
                modifier = Modifier.padding(start = 26.dp, top = 4.dp).clickable { all = true },
            )
        }
    }
}

/**
 * A row of the plan: its start position, task, item and status. Queued
 * rows move up and down in place; a tap offers the rest.
 */
@Composable
private fun PlanRowView(
    r: PlanItem,
    off: String?,
    busy: Boolean,
    onMove: (Long) -> Unit,
    onUnqueue: () -> Unit,
    onEnqueue: () -> Unit,
    onClear: () -> Unit,
    onOpen: () -> Unit,
    onCopy: () -> Unit,
    onAgent: (queue: Boolean) -> Unit,
) {
    var menu by remember { mutableStateOf(false) }
    val can = off == null && !busy
    Box {
        Row(
            Modifier.fillMaxWidth().clickable { menu = true }.padding(start = 16.dp, top = 8.dp, bottom = 8.dp, end = 4.dp)
                .alpha(if (r.quiet) 0.6f else 1f).testTag("plan-row:" + r.id),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(r.position?.toString() ?: "", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.width(24.dp))
            Column(Modifier.weight(1f)) {
                Text(r.title.ifEmpty { r.ref }, style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(
                    listOf(r.task.ifEmpty { "—" }, r.ref).joinToString(" · "),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                if (r.status.isNotEmpty()) {
                    Text(r.status, style = MaterialTheme.typography.bodySmall, color = r.tone.color(), maxLines = 1,
                        overflow = TextOverflow.Ellipsis, modifier = Modifier.testTag("plan-status:" + r.id))
                }
            }
            if (r.queued != null) {
                IconButton(onClick = { onMove(-1) }, enabled = can, modifier = Modifier.testTag("up:" + r.id)) {
                    Icon(Icons.Filled.KeyboardArrowUp, contentDescription = "Start earlier")
                }
                IconButton(onClick = { onMove(1) }, enabled = can, modifier = Modifier.testTag("down:" + r.id)) {
                    Icon(Icons.Filled.KeyboardArrowDown, contentDescription = "Start later")
                }
            } else {
                Spacer(Modifier.width(12.dp))
            }
        }
        DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
            if (r.url != null) {
                DropdownMenuItem(text = { Text("Open on GitHub") }, onClick = { menu = false; onOpen() })
                DropdownMenuItem(text = { Text("Copy link") }, onClick = { menu = false; onCopy() })
                HorizontalDivider()
            }
            DropdownMenuItem(text = { Text("Start agent…") }, enabled = can && !r.missing, onClick = { menu = false; onAgent(false) })
            when {
                r.queued != null -> DropdownMenuItem(
                    text = { Text("Remove from queue") },
                    enabled = can,
                    onClick = { menu = false; onUnqueue() },
                    modifier = Modifier.testTag("unqueue:" + r.id),
                )
                r.task.isNotEmpty() -> DropdownMenuItem(
                    text = { Text("Queue ${r.task}") },
                    enabled = can,
                    onClick = { menu = false; onEnqueue() },
                    modifier = Modifier.testTag("enqueue:" + r.id),
                )
                else -> DropdownMenuItem(text = { Text("Add to queue…") }, enabled = can, onClick = { menu = false; onAgent(true) })
            }
            if (r.gated) {
                HorizontalDivider()
                DropdownMenuItem(text = { Text("Clear gate") }, enabled = can, onClick = { menu = false; onClear() })
            }
        }
    }
}

/** Asks for an epic to enroll. onDone gets what to say, or null when cancelled. */
@Composable
private fun EnrollDialog(plan: PlanActions, onDone: (String?) -> Unit) {
    var target by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val scope = rememberCoroutineScope()
    AlertDialog(
        onDismissRequest = { onDone(null) },
        title = { Text("Enroll an epic") },
        text = {
            Column {
                Text("gh-hud reads the issue as a plan of pieces. Nothing starts until you approve the plan.")
                OutlinedTextField(
                    target,
                    onValueChange = { target = it },
                    placeholder = { Text("Issue URL or owner/repo#N") },
                    singleLine = true,
                    modifier = Modifier.padding(top = 8.dp).testTag("enroll-target"),
                )
                error?.let { Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.padding(top = 4.dp).testTag("enroll-error")) }
            }
        },
        confirmButton = {
            TextButton(
                onClick = {
                    busy = true
                    error = null
                    scope.launch {
                        when (val a = plan.enroll(target)) {
                            is Answer.Ok -> onDone(a.value)
                            is Answer.No -> error = a.why
                        }
                        busy = false
                    }
                },
                enabled = target.isNotBlank() && !busy,
                modifier = Modifier.testTag("enroll-go"),
            ) { Text("Enroll") }
        },
        dismissButton = { TextButton(onClick = { onDone(null) }) { Text("Cancel") } },
    )
}
