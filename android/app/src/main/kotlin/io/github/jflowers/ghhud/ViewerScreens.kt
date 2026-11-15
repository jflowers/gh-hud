package io.github.jflowers.ghhud

import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.jflowers.ghhud.api.v1.TranscriptResponse
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.HubSummary
import io.github.jflowers.ghhud.core.PaneRead
import io.github.jflowers.ghhud.core.Tone
import io.github.jflowers.ghhud.core.Viewers
import io.github.jflowers.ghhud.core.WatchTarget
import kotlinx.coroutines.flow.Flow
import java.time.Instant

/** What the viewers ask the hub for. */
interface ViewerActions {
    /** The pane, read now and every two seconds while collected. */
    fun follow(t: WatchTarget): Flow<PaneRead>
    suspend fun transcript(runId: String): Answer<TranscriptResponse>
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun ViewerFrame(title: String, onBack: () -> Unit, actions: @Composable () -> Unit, content: @Composable (Modifier) -> Unit) {
    Scaffold(
        contentWindowInsets = WindowInsets(0),
        topBar = {
            TopAppBar(
                title = { Text(title, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.testTag("viewer-title")) },
                navigationIcon = {
                    IconButton(onClick = onBack, modifier = Modifier.testTag("back")) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },
                actions = { actions() },
            )
        },
    ) { inner -> content(Modifier.padding(inner).fillMaxSize()) }
}

/**
 * Watch: the agent's pane as the Mac's terminal shows it, read again every
 * two seconds and kept at its end. Pause stops reading so you can read
 * back; the pane's lines don't wrap, so scroll sideways for wide ones.
 */
@Composable
fun WatchScreen(t: WatchTarget, act: ViewerActions, onBack: () -> Unit) {
    var paused by remember { mutableStateOf(false) }
    var read by remember { mutableStateOf<PaneRead?>(null) }
    var text by remember { mutableStateOf("") }
    LaunchedEffect(t, paused) {
        if (paused) return@LaunchedEffect
        act.follow(t).collect { r ->
            read = r
            r.text?.let { text = it }
        }
    }
    val down = rememberScrollState()
    LaunchedEffect(text) { if (!paused) down.scrollTo(down.maxValue) }
    ViewerFrame(
        "Watch · ${t.title}",
        onBack,
        actions = {
            TextButton(onClick = { paused = !paused }, modifier = Modifier.testTag("pause")) {
                Text(if (paused) "Follow" else "Pause")
            }
        },
    ) { m ->
        Column(m) {
            val r = read
            val status = when {
                paused -> "Paused"
                r == null -> "Reading the pane…"
                r.error != null -> "Can't read the pane: ${r.error}"
                else -> "Updated ${HubSummary.ago(r.at, Instant.now())}"
            }
            Text(
                status,
                style = MaterialTheme.typography.bodySmall,
                color = if (r?.error != null && !paused) Tone.BAD.color() else Tone.MUTED.color(),
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp).testTag("watch-status"),
            )
            HorizontalDivider()
            Text(
                text,
                fontFamily = FontFamily.Monospace,
                fontSize = 11.sp,
                lineHeight = 14.sp,
                softWrap = false,
                modifier = Modifier.fillMaxSize().verticalScroll(down).horizontalScroll(rememberScrollState())
                    .padding(8.dp).testTag("pane"),
            )
        }
    }
}

/** Transcript: the run's conversation, its last messages, newest at the bottom. */
@Composable
fun TranscriptScreen(runId: String, title: String, act: ViewerActions, onBack: () -> Unit) {
    var answer by remember { mutableStateOf<Answer<TranscriptResponse>?>(null) }
    var reload by remember { mutableIntStateOf(0) }
    LaunchedEffect(runId, reload) {
        answer = null
        answer = act.transcript(runId)
    }
    ViewerFrame(
        "Transcript · $title",
        onBack,
        actions = {
            IconButton(onClick = { reload++ }, modifier = Modifier.testTag("reload")) {
                Icon(Icons.Filled.Refresh, contentDescription = "Read it again")
            }
        },
    ) { m ->
        when (val a = answer) {
            null -> Column(m.padding(24.dp)) { CircularProgressIndicator() }
            is Answer.No -> Text(
                "Can't read the transcript: ${a.why}",
                color = Tone.BAD.color(),
                modifier = m.padding(16.dp).testTag("transcript-error"),
            )
            is Answer.Ok -> Messages(a.value, m)
        }
    }
}

@Composable
private fun Messages(t: TranscriptResponse, m: Modifier) {
    val list = rememberLazyListState()
    LaunchedEffect(t) { if (t.messagesCount > 0) list.scrollToItem(t.messagesCount) }
    LazyColumn(m.testTag("transcript"), state = list) {
        item {
            Viewers.omitted(t.omitted)?.let {
                Text(it, style = MaterialTheme.typography.bodySmall, color = Tone.MUTED.color(),
                    modifier = Modifier.padding(16.dp).testTag("omitted"))
            }
            if (t.messagesCount == 0) {
                Text("The session has no messages yet.", color = Tone.MUTED.color(), modifier = Modifier.padding(16.dp))
            }
        }
        items(t.messagesList) { msg ->
            Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp)) {
                Text(
                    Viewers.heading(msg),
                    style = MaterialTheme.typography.labelMedium,
                    color = if (msg.role == "user") MaterialTheme.colorScheme.primary else Tone.MUTED.color(),
                )
                for (p in msg.partsList) {
                    if (p.tool.isEmpty()) {
                        Text(p.text, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.padding(top = 2.dp))
                    } else {
                        Text(
                            "▸ " + Viewers.toolLine(p),
                            fontFamily = FontFamily.Monospace,
                            fontSize = 12.sp,
                            color = if (p.failed) Tone.BAD.color() else Tone.MUTED.color(),
                            maxLines = 2,
                            overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.padding(top = 2.dp),
                        )
                    }
                }
            }
            HorizontalDivider()
        }
    }
}
