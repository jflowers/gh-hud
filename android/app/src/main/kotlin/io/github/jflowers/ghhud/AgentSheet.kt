package io.github.jflowers.ghhud

import androidx.compose.foundation.background
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.TextButton
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import io.github.jflowers.ghhud.core.NamedItem
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.material3.OutlinedCard
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.jflowers.ghhud.core.AgentChoice
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.ItemRow
import kotlinx.coroutines.launch

/**
 * The agent tasks for an item, from the hub: where one can start now, or
 * what can join your queue. Picking one runs it; onDone gets what to say,
 * or null when you closed the sheet.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AgentSheet(row: ItemRow, queue: Boolean, act: ItemActions, onDone: (String?) -> Unit) {
    var choices by remember { mutableStateOf<Answer<List<AgentChoice>>?>(null) }
    var running by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<Pair<Answer.No, AgentChoice>?>(null) }
    val scope = rememberCoroutineScope()
    val start: (AgentChoice) -> Unit = { choice ->
        running = choice.id
        error = null
        scope.launch {
            when (val r = act.run(row, choice, queue)) {
                is Answer.Ok -> onDone(r.value)
                is Answer.No -> error = r to choice
            }
            running = null
        }
    }
    LaunchedEffect(row.id, queue) { choices = act.choices(row, queue) }
    ModalBottomSheet(onDismissRequest = { onDone(null) }, modifier = Modifier.testTag("agent-sheet")) {
        Column(Modifier.padding(horizontal = 16.dp).padding(bottom = 24.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(if (queue) "Add to your queue" else "Start an agent", style = MaterialTheme.typography.titleMedium)
            Text(
                "${row.ref} · ${row.title}",
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
            )
            when (val c = choices) {
                null -> Row(verticalAlignment = Alignment.CenterVertically) {
                    CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                    Text(
                        if (queue) "Asking the hub what can be queued…" else "Asking the hub where an agent can start…",
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(start = 8.dp),
                    )
                }
                is Answer.No -> Text(c.why, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.testTag("choices-none"))
                is Answer.Ok -> ChoiceCards(c.value, running, showSuggested = false, pick = start)
            }
            error?.let { (no, choice) -> SheetError(no, enabled = running == null) { start(choice) } }
        }
    }
}

/**
 * Why starting failed, and Try again. Not when the call was cut off: the
 * agent may be starting, and the words say to check Agents first.
 */
@Composable
private fun SheetError(no: Answer.No, enabled: Boolean, retry: () -> Unit) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text(
            no.why,
            color = MaterialTheme.colorScheme.error,
            modifier = Modifier.weight(1f).testTag("sheet-error"),
        )
        if (!no.unsure) {
            TextButton(onClick = retry, enabled = enabled, modifier = Modifier.testTag("try-again")) { Text("Try again") }
        }
    }
}

/** The choices as cards; picking one calls pick. */
@Composable
private fun ChoiceCards(choices: List<AgentChoice>, running: String?, showSuggested: Boolean, pick: (AgentChoice) -> Unit) {
    for (choice in choices) {
        OutlinedCard(
            onClick = { pick(choice) },
            enabled = running == null,
            modifier = Modifier.fillMaxWidth().testTag("choice:" + choice.id),
        ) {
            Row(Modifier.padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        Text(choice.task, style = MaterialTheme.typography.bodyLarge)
                        if (showSuggested && choice.suggested) {
                            Text(
                                "suggested",
                                style = MaterialTheme.typography.labelSmall,
                                color = MaterialTheme.colorScheme.onSecondaryContainer,
                                modifier = Modifier
                                    .background(MaterialTheme.colorScheme.secondaryContainer, RoundedCornerShape(50))
                                    .padding(horizontal = 6.dp, vertical = 1.dp)
                                    .testTag("suggested:" + choice.task),
                            )
                        }
                    }
                    if (choice.cmd.isNotEmpty()) {
                        Text(
                            choice.cmd,
                            style = MaterialTheme.typography.bodySmall,
                            fontFamily = FontFamily.Monospace,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                        )
                    }
                }
                if (running == choice.id) {
                    CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                } else {
                    Text(choice.target, color = MaterialTheme.colorScheme.onSurfaceVariant)
                }
            }
        }
    }
}

/**
 * Start an agent on any issue or PR: you name it (a link, or
 * owner/repo#N; initial fills the field and is looked up at once), the
 * hub says what it is and offers every task that fits, the ones it looks
 * like it needs marked. onDone gets what to say, or null when closed.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun NamedAgentSheet(initial: String, act: ItemActions, onDone: (String?) -> Unit) {
    var text by remember { mutableStateOf(initial) }
    var looking by remember { mutableStateOf(false) }
    var found by remember { mutableStateOf<Answer<Pair<NamedItem, List<AgentChoice>>>?>(null) }
    var running by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf<Pair<Answer.No, AgentChoice>?>(null) }
    val scope = rememberCoroutineScope()
    val lookUp = {
        if (text.isNotBlank() && !looking) {
            looking = true
            found = null
            error = null
            scope.launch {
                found = act.lookUp(text)
                looking = false
            }
        }
    }
    LaunchedEffect(Unit) { if (initial.isNotBlank()) lookUp() }
    // Fully open: the field brings up the keyboard, and the choices come
    // under it.
    val state = rememberModalBottomSheetState(skipPartiallyExpanded = true)
    ModalBottomSheet(onDismissRequest = { onDone(null) }, sheetState = state, modifier = Modifier.testTag("named-sheet")) {
        Column(Modifier.padding(horizontal = 16.dp).padding(bottom = 24.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("Start an agent", style = MaterialTheme.typography.titleMedium)
            OutlinedTextField(
                value = text,
                onValueChange = { text = it },
                label = { Text("Issue or PR link, or owner/repo#N") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri, imeAction = ImeAction.Search),
                keyboardActions = KeyboardActions(onSearch = { lookUp() }),
                trailingIcon = {
                    TextButton(onClick = { lookUp() }, enabled = text.isNotBlank() && !looking && running == null, modifier = Modifier.testTag("look-up")) {
                        Text("Look up")
                    }
                },
                modifier = Modifier.fillMaxWidth().testTag("named-target"),
            )
            when (val f = found) {
                null -> if (looking) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        CircularProgressIndicator(Modifier.size(18.dp), strokeWidth = 2.dp)
                        Text("Asking the hub about it…", color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.padding(start = 8.dp))
                    }
                } else {
                    Text(
                        "Paste a link to any open issue or pull request, or share one to gh-hud from your browser. " +
                            "The hub offers every task that fits it, the ones it looks like it needs first.",
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
                is Answer.No -> Text(f.why, color = MaterialTheme.colorScheme.onSurfaceVariant, modifier = Modifier.testTag("choices-none"))
                is Answer.Ok -> {
                    val (item, choices) = f.value
                    Text(
                        item.line,
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        maxLines = 2,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.testTag("named-item"),
                    )
                    val start: (AgentChoice) -> Unit = { choice ->
                        running = choice.id
                        error = null
                        scope.launch {
                            when (val r = act.run(item, choice)) {
                                is Answer.Ok -> onDone(r.value)
                                is Answer.No -> error = r to choice
                            }
                            running = null
                        }
                    }
                    ChoiceCards(choices, running, showSuggested = true, pick = start)
                    error?.let { (no, choice) -> SheetError(no, enabled = running == null) { start(choice) } }
                }
            }
        }
    }
}
