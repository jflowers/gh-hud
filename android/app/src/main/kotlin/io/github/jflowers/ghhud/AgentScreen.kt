package io.github.jflowers.ghhud

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.Button
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.jflowers.ghhud.core.AgentDetail
import io.github.jflowers.ghhud.core.AgentRow
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.DeweyIndex
import io.github.jflowers.ghhud.core.Links
import io.github.jflowers.ghhud.core.Tone
import io.github.jflowers.ghhud.core.WatchTarget
import io.github.jflowers.ghhud.core.WorkspaceDetail
import io.github.jflowers.ghhud.core.WorkspaceRow
import kotlinx.coroutines.launch
import java.time.Instant

/** What an agent's screen can ask for, beyond opening and copying. */
interface AgentActions {
    suspend fun recheck(r: AgentRow): Answer<String>
}

/** A detail screen's frame: a title and Back. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun Detail(title: String, onBack: () -> Unit, content: @Composable () -> Unit) {
    Scaffold(
        contentWindowInsets = WindowInsets(0),
        topBar = {
            TopAppBar(
                title = { Text(title, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.testTag("detail-title")) },
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
    ) { inner ->
        Column(Modifier.padding(inner).fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp)) { content() }
    }
}

/**
 * An agent session: its run's outcome for a dispatch, the session's
 * identity otherwise, and its commands: Watch while it runs, its
 * Transcript, Re-check once it's done.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun AgentScreen(
    row: AgentRow?,
    off: String?,
    act: AgentActions,
    items: ItemActions,
    say: Say,
    onBack: () -> Unit,
    onWatch: (WatchTarget) -> Unit = {},
    onTranscript: (runId: String, title: String) -> Unit = { _, _ -> },
    now: Instant = Instant.now(),
) {
    val scope = rememberCoroutineScope()
    var rechecking by remember { mutableStateOf(false) }
    Detail(row?.name ?: "Agent", onBack) {
        if (row == null) {
            Text("This session is no longer in the hub's list.", modifier = Modifier.testTag("agent-gone"))
            return@Detail
        }
        val (head, tone) = AgentDetail.headline(row)
        Text(head, style = MaterialTheme.typography.titleLarge, color = tone.color(), modifier = Modifier.testTag("headline"))
        Text(row.name, style = MaterialTheme.typography.bodyMedium, color = Tone.MUTED.color())
        Column(Modifier.padding(vertical = 12.dp)) {
            for (f in AgentDetail.facts(row, now)) FactRow(f.label, f.value)
        }
        row.run?.reason?.takeIf { it.isNotEmpty() }?.let {
            Text("⚠ $it", color = Tone.BAD.color(), modifier = Modifier.testTag("reason"))
        }
        for (l in AgentDetail.lists(row)) FactLines(l)
        AgentDetail.finalMessage(row)?.let {
            Text("Final message", style = MaterialTheme.typography.titleSmall, modifier = Modifier.padding(top = 12.dp))
            Text(it, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.testTag("final"))
        }
        off?.let { Text(it, color = Tone.MUTED.color(), modifier = Modifier.padding(top = 12.dp).testTag("off")) }
        FlowRow(
            Modifier.padding(top = 12.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalArrangement = Arrangement.spacedBy(4.dp),
        ) {
            // As on the Mac: any live agent; the hub says so when it has no pane.
            if (row.live) {
                Button(onClick = { onWatch(WatchTarget.of(row)) }, modifier = Modifier.testTag("watch")) { Text("Watch") }
            }
            row.run?.takeIf { it.sessionId.isNotEmpty() }?.let { run ->
                OutlinedButton(onClick = { onTranscript(run.id, row.name) }, modifier = Modifier.testTag("transcript")) {
                    Text("Transcript")
                }
            }
            row.run?.takeIf { it.hasFinishedAt() }?.let { run ->
                OutlinedButton(
                    onClick = {
                        rechecking = true
                        say("Re-checking ${run.task} on ${run.ref}…", false)
                        scope.launch {
                            when (val a = act.recheck(row)) {
                                is Answer.Ok -> say(a.value, false)
                                is Answer.No -> say(a.why, true)
                            }
                            rechecking = false
                        }
                    },
                    enabled = off == null && !rechecking,
                    modifier = Modifier.testTag("recheck"),
                ) { Text("Re-check run") }
            }
            Links.forRef(row.session.ref)?.let { url ->
                OutlinedButton(onClick = { items.open(url) }, modifier = Modifier.testTag("open")) { Text("Open ${row.session.ref}") }
            }
            OutlinedButton(onClick = { items.copy(row.session.id) }) { Text("Copy session ID") }
            if (row.session.directory.isNotEmpty()) {
                OutlinedButton(onClick = { items.copy(row.session.directory) }) { Text("Copy directory") }
            }
        }
    }
}

/** A checkout: why it is or isn't ready for the next run. */
@Composable
fun WorkspaceScreen(row: WorkspaceRow?, items: ItemActions, onBack: () -> Unit, now: Instant = Instant.now()) {
    Detail(row?.place ?: "Workspace", onBack) {
        if (row == null) {
            Text("This checkout is no longer in the hub's list.", modifier = Modifier.testTag("workspace-gone"))
            return@Detail
        }
        Text(row.health.repo, style = MaterialTheme.typography.bodyMedium, color = Tone.MUTED.color())
        Text(WorkspaceDetail.sentence(row), style = MaterialTheme.typography.titleMedium, color = row.tone.color(),
            modifier = Modifier.padding(top = 4.dp).testTag("sentence"))
        Column(Modifier.padding(vertical = 12.dp)) {
            for (f in WorkspaceDetail.facts(row)) FactRow(f.label, f.value)
        }
        for (l in WorkspaceDetail.lists(row)) FactLines(l)
        val facts = DeweyIndex.facts(row.health, now)
        if (facts.isNotEmpty()) {
            Text("Dewey index", style = MaterialTheme.typography.titleSmall, modifier = Modifier.padding(top = 12.dp).testTag("dewey"))
            DeweyIndex.state(row.health, now)?.let { (text, tone) ->
                Text(text, style = MaterialTheme.typography.bodyMedium, color = tone.color(), modifier = Modifier.testTag("index-state"))
            }
            Column(Modifier.padding(vertical = 4.dp)) {
                for (f in facts) FactRow(f.label, f.value)
            }
            for (l in DeweyIndex.lists(row.health)) FactLines(l)
        }
        OutlinedButton(onClick = { items.copy(row.health.dir) }, modifier = Modifier.padding(top = 12.dp)) { Text("Copy directory") }
    }
}
