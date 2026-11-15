package io.github.jflowers.ghhud

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
    var error by remember { mutableStateOf<String?>(null) }
    val scope = rememberCoroutineScope()
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
                is Answer.Ok -> for (choice in c.value) {
                    OutlinedCard(
                        onClick = {
                            running = choice.id
                            error = null
                            scope.launch {
                                when (val a = act.run(row, choice, queue)) {
                                    is Answer.Ok -> onDone(a.value)
                                    is Answer.No -> error = a.why
                                }
                                running = null
                            }
                        },
                        enabled = running == null,
                        modifier = Modifier.fillMaxWidth().testTag("choice:" + choice.id),
                    ) {
                        Row(Modifier.padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
                            Column(Modifier.weight(1f)) {
                                Text(choice.task, style = MaterialTheme.typography.bodyLarge)
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
            error?.let { Text(it, color = MaterialTheme.colorScheme.error, modifier = Modifier.testTag("sheet-error")) }
        }
    }
}
