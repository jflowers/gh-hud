package io.github.jflowers.ghhud

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
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
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import io.github.jflowers.ghhud.core.AgentChoice
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.HubSummary
import io.github.jflowers.ghhud.core.ItemRow
import io.github.jflowers.ghhud.core.NamedItem
import io.github.jflowers.ghhud.core.WatchTarget
import kotlinx.coroutines.launch
import java.time.Instant

/** What an item's screen can ask for. */
interface ItemActions {
    fun open(url: String)
    fun copy(text: String)
    suspend fun choices(row: ItemRow, queue: Boolean): Answer<List<AgentChoice>>
    suspend fun run(row: ItemRow, c: AgentChoice, queue: Boolean): Answer<String>
    suspend fun recheck(row: ItemRow): Answer<String>

    /** An issue or PR you name, and every task that can start on it. */
    suspend fun lookUp(text: String): Answer<Pair<NamedItem, List<AgentChoice>>> = Answer.No("Not available")

    suspend fun run(item: NamedItem, c: AgentChoice): Answer<String> = Answer.No("Not available")
}

/** A command's outcome, for the snackbar: failures stay until dismissed. */
typealias Say = (text: String, bad: Boolean) -> Unit

/** One item: everything its row has, untruncated, and its commands. */
@OptIn(ExperimentalMaterial3Api::class, ExperimentalLayoutApi::class)
@Composable
fun ItemScreen(
    row: ItemRow?,
    off: String?,
    act: ItemActions,
    say: Say,
    onBack: () -> Unit,
    onWatch: (WatchTarget) -> Unit = {},
    onTranscript: (runId: String, title: String) -> Unit = { _, _ -> },
    now: Instant = Instant.now(),
) {
    var sheet by remember { mutableStateOf<Boolean?>(null) } // queue or start
    val scope = rememberCoroutineScope()
    var rechecking by remember { mutableStateOf(false) }
    Scaffold(
        contentWindowInsets = WindowInsets(0),
        topBar = {
            TopAppBar(
                title = { Text(row?.ref ?: "Item", modifier = Modifier.testTag("item-title")) },
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
            )
        },
    ) { inner ->
        if (row == null) {
            Text(
                "This item is no longer in the hub's list.",
                modifier = Modifier.padding(inner).padding(24.dp).testTag("item-gone"),
            )
            return@Scaffold
        }
        Column(Modifier.padding(inner).fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp)) {
            Text(row.title, style = MaterialTheme.typography.titleLarge)
            row.marker?.let { Text("◆ ${it.words}", color = it.color(), modifier = Modifier.padding(top = 4.dp)) }
            Column(Modifier.padding(vertical = 12.dp)) {
                FactRow("Kind", row.kind + if (row.draft) ", draft" else "")
                FactRow("Repo", row.repo)
                if (row.author.isNotEmpty()) FactRow("Author", row.author)
                if (row.statusWords.isNotEmpty()) FactRow("Status", row.statusWords.joinToString(", "))
                if (row.threads > 0 || row.botThreads > 0) {
                    FactRow("Threads", "${row.threads} from people, ${row.botThreads} from bots, unresolved")
                }
                if (row.lastActor.isNotEmpty()) FactRow("Last", row.lastActor)
                row.updated?.let { FactRow("Updated", HubSummary.ago(it, now)) }
                if (row.labels.isNotEmpty()) FactRow("Labels", row.labels.joinToString(", "))
                row.run?.let { FactRow("Agent run", "${it.text(now)}: ${it.words}", it.tone.color()) }
                row.stage?.let { FactRow("Next", it.text, it.tone.color()) }
            }
            off?.let {
                Text(it, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(bottom = 8.dp).testTag("off"))
            }
            FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                Button(onClick = { act.open(row.id) }, modifier = Modifier.testTag("open")) { Text("Open on GitHub") }
                OutlinedButton(onClick = { sheet = false }, enabled = off == null, modifier = Modifier.testTag("start")) {
                    Text("Start agent…")
                }
                OutlinedButton(onClick = { sheet = true }, enabled = off == null, modifier = Modifier.testTag("queue")) {
                    Text("Add to queue…")
                }
                row.latestRun?.takeIf { it.live }?.let { run ->
                    OutlinedButton(onClick = { WatchTarget.of(row)?.let(onWatch) }, modifier = Modifier.testTag("watch")) {
                        Text("Watch ${run.task} run")
                    }
                }
                row.latestRun?.takeIf { it.hasTranscript }?.let { run ->
                    OutlinedButton(onClick = { onTranscript(run.id, "${run.task} ${row.ref}") }, modifier = Modifier.testTag("transcript")) {
                        Text("${run.task} transcript")
                    }
                }
                row.latestRun?.takeIf { it.finished }?.let { run ->
                    OutlinedButton(
                        onClick = {
                            rechecking = true
                            say("Re-checking ${run.task} on ${row.ref}…", false)
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
                    ) { Text("Re-check ${run.task} run") }
                }
                OutlinedButton(onClick = { act.copy(row.id) }) { Text("Copy link") }
            }
        }
    }
    sheet?.let { queue ->
        AgentSheet(row ?: return@let, queue, act, onDone = { said ->
            sheet = null
            said?.let { say(it, false) }
        })
    }
}
