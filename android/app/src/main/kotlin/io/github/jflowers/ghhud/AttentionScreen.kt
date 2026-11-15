package io.github.jflowers.ghhud

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.pulltorefresh.PullToRefreshBox
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.jflowers.ghhud.core.Approval
import io.github.jflowers.ghhud.core.AttentionRow
import io.github.jflowers.ghhud.core.HubAddress
import io.github.jflowers.ghhud.core.HubStatus
import io.github.jflowers.ghhud.core.HubSummary
import java.time.Instant

/** What the screen can ask for. */
interface AttentionActions {
    fun open(url: String)
    fun approve(row: AttentionRow)
    fun reject(row: AttentionRow)
    fun refresh()
    fun setAddress(a: HubAddress)
    fun setNotify(on: Boolean)
}

/** The hub's state in a line: its summary when connected, why not otherwise. */
fun statusLine(ui: Ui, now: Instant = Instant.now()): String = when (val s = ui.hub.status) {
    HubStatus.NotSet -> "No hub set"
    HubStatus.Connecting -> "Connecting to ${ui.address.url}…"
    HubStatus.Connected -> HubSummary.line(ui.hub.snapshot, now)
    is HubStatus.Lost -> "Lost the hub: ${s.why}. Retrying…"
}

/** What needs you, by section, with approvals under their rows. */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AttentionScreen(ui: Ui, act: AttentionActions) {
    var editing by remember { mutableStateOf(false) }
    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    val n = ui.hub.attention.count
                    Text(if (n > 0) "gh-hud · $n" else "gh-hud", modifier = Modifier.testTag("title"))
                },
                actions = { TextButton(onClick = { editing = true }) { Text("Hub") } },
            )
        },
    ) { inner ->
        Column(Modifier.padding(inner).fillMaxSize()) {
            Text(
                statusLine(ui),
                style = MaterialTheme.typography.bodySmall,
                color = if (ui.hub.status is HubStatus.Lost) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp).testTag("status"),
            )
            ui.refreshError?.let {
                Text("Refresh failed: $it", color = MaterialTheme.colorScheme.error, modifier = Modifier.padding(horizontal = 16.dp))
            }
            if (ui.hub.status == HubStatus.NotSet) {
                NoHub(onSet = { editing = true })
            } else {
                PullToRefreshBox(
                    isRefreshing = ui.refreshing,
                    onRefresh = act::refresh,
                    modifier = Modifier.fillMaxSize(),
                ) {
                    Sections(ui, act)
                }
            }
        }
    }
    if (editing) {
        AddressDialog(ui.address, ui.notify, onDismiss = { editing = false }, onSave = { a, notify ->
            editing = false
            act.setAddress(a)
            if (notify != ui.notify) act.setNotify(notify)
        })
    }
}

@Composable
private fun NoHub(onSet: () -> Unit) {
    Column(
        Modifier.fillMaxSize().padding(24.dp),
        verticalArrangement = Arrangement.Center,
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text("Where's your hub?", style = MaterialTheme.typography.titleMedium)
        Text(
            "Its address on your tailnet, like https://gh-hud.your-tailnet.ts.net",
            style = MaterialTheme.typography.bodyMedium,
            modifier = Modifier.padding(vertical = 8.dp),
        )
        Button(onClick = onSet) { Text("Set the hub") }
    }
}

@Composable
private fun Sections(ui: Ui, act: AttentionActions) {
    val sections = ui.hub.attention.sections
    val now = Instant.now()
    LazyColumn(Modifier.fillMaxSize().testTag("sections")) {
        if (sections.isEmpty() && ui.hub.status == HubStatus.Connected) {
            item {
                Text(
                    "Nothing needs you.",
                    style = MaterialTheme.typography.bodyLarge,
                    modifier = Modifier.padding(24.dp).testTag("empty"),
                )
            }
        }
        for (section in sections) {
            item(key = "section:" + section.id) {
                Text(
                    "${section.title} (${section.rows.size})",
                    style = MaterialTheme.typography.titleSmall,
                    color = MaterialTheme.colorScheme.primary,
                    modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 16.dp, bottom = 4.dp),
                )
            }
            items(section.rows, key = { it.id }) { row ->
                RowView(row, pending = row.id in ui.pending, error = ui.errors[row.id], now = now, act = act)
                HorizontalDivider()
            }
        }
    }
}

@Composable
private fun RowView(row: AttentionRow, pending: Boolean, error: String?, now: Instant, act: AttentionActions) {
    val open = row.url?.let { url -> Modifier.clickable { act.open(url) } } ?: Modifier
    Column(open.fillMaxWidth().padding(horizontal = 16.dp, vertical = 10.dp).testTag("row:" + row.id)) {
        Row(verticalAlignment = Alignment.Top) {
            Text(
                row.title,
                style = MaterialTheme.typography.bodyLarge,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f),
            )
            row.updated?.let {
                Text(
                    HubSummary.ago(it, now),
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(start = 8.dp),
                )
            }
        }
        Text(
            row.detail,
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
        row.approval?.let { a -> Decision(row, a, pending, act) }
        error?.let {
            Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall, modifier = Modifier.testTag("error:" + row.id))
        }
    }
}

@Composable
private fun Decision(row: AttentionRow, a: Approval, pending: Boolean, act: AttentionActions) {
    Row(Modifier.padding(top = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        if (pending) {
            CircularProgressIndicator(Modifier.width(20.dp), strokeWidth = 2.dp)
            Text("Sent", style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(start = 8.dp))
            return@Row
        }
        Button(onClick = { act.approve(row) }, modifier = Modifier.testTag("approve:" + row.id)) { Text(a.verb) }
        if (a.rejectable) {
            Spacer(Modifier.width(8.dp))
            OutlinedButton(onClick = { act.reject(row) }, modifier = Modifier.testTag("reject:" + row.id)) {
                Text("Reject")
            }
        }
    }
}

@Composable
private fun AddressDialog(
    current: HubAddress,
    notifying: Boolean,
    onDismiss: () -> Unit,
    onSave: (HubAddress, Boolean) -> Unit,
) {
    var url by remember { mutableStateOf(current.url) }
    var token by remember { mutableStateOf(current.token) }
    var notify by remember { mutableStateOf(notifying) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("Hub") },
        text = {
            Box {
                Column {
                    OutlinedTextField(
                        url,
                        onValueChange = { url = it },
                        label = { Text("URL") },
                        placeholder = { Text("https://gh-hud.your-tailnet.ts.net") },
                        singleLine = true,
                        keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                        modifier = Modifier.testTag("url"),
                    )
                    OutlinedTextField(
                        token,
                        onValueChange = { token = it },
                        label = { Text("Token (loopback hub only)") },
                        singleLine = true,
                        visualTransformation = PasswordVisualTransformation(),
                        modifier = Modifier.padding(top = 8.dp),
                    )
                    Row(Modifier.padding(top = 12.dp), verticalAlignment = Alignment.CenterVertically) {
                        Column(Modifier.weight(1f)) {
                            Text("Notify me", style = MaterialTheme.typography.bodyLarge)
                            Text(
                                "Stays connected while the app is closed",
                                style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                        Switch(notify, onCheckedChange = { notify = it }, modifier = Modifier.testTag("notify"))
                    }
                }
            }
        },
        confirmButton = { TextButton(onClick = { onSave(HubAddress(url, token), notify) }, modifier = Modifier.testTag("save")) { Text("Save") } },
        dismissButton = { TextButton(onClick = onDismiss) { Text("Cancel") } },
    )
}
