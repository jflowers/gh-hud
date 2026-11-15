package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.TranscriptMessage
import io.github.jflowers.ghhud.api.v1.TranscriptPart
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import java.time.Instant
import kotlin.time.Duration
import kotlin.time.Duration.Companion.seconds

// Watch and Transcript on the phone: the content of the Mac's terminal
// windows, from the hub (ReadPane and Transcript).

/** What to watch: a run's pane (run IDs are unique), else a session's. */
public data class WatchTarget(val runId: String, val sessionId: String, val title: String) {
    /** For the app's navigation: "run:<id>" or "session:<id>". */
    val key: String get() = if (runId.isNotEmpty()) "run:$runId" else "session:$sessionId"

    public companion object {
        public fun of(r: AgentRow): WatchTarget = WatchTarget(r.run?.id.orEmpty(), r.session.id, r.name)

        public fun of(row: ItemRow): WatchTarget? = row.latestRun?.let { WatchTarget(it.id, "", "${it.task} ${row.ref}") }

        public fun parse(key: String, title: String = ""): WatchTarget? = when {
            key.startsWith("run:") -> WatchTarget(key.removePrefix("run:"), "", title)
            key.startsWith("session:") -> WatchTarget("", key.removePrefix("session:"), title)
            else -> null
        }
    }
}

/** One read of a pane: its text when it worked, why not otherwise. */
public data class PaneRead(val text: String?, val error: String?, val at: Instant)

public object Viewers {
    /** How often Watch reads the pane again. */
    public val EVERY: Duration = 2.seconds

    /**
     * Reads the pane now and then every [every] until the collector stops:
     * Watch stops reading when its screen goes. A failed read is reported
     * and the next one tried, so a hub that comes back shows again.
     */
    public fun follow(hub: HubConnection, t: WatchTarget, every: Duration = EVERY, lines: Int = 400): Flow<PaneRead> = flow {
        while (true) {
            emit(
                when (val a = hub.readPane(t.runId, t.sessionId, lines)) {
                    is Answer.Ok -> PaneRead(a.value, null, Instant.now())
                    is Answer.No -> PaneRead(null, a.why, Instant.now())
                },
            )
            delay(every)
        }
    }

    /** "assistant · 13:05". */
    public fun heading(m: TranscriptMessage, zone: java.time.ZoneId = java.time.ZoneId.systemDefault()): String {
        val who = if (m.role == "user") "you" else m.role.ifEmpty { "agent" }
        if (!m.hasCreated()) return who
        val t = Instant.ofEpochSecond(m.created.seconds, m.created.nanos.toLong())
        return who + " · " + java.time.format.DateTimeFormatter.ofPattern("MMM d, HH:mm", java.util.Locale.US).withZone(zone).format(t)
    }

    /** A tool call on one line: "bash  go test ./..." (with "failed" when it did). */
    public fun toolLine(p: TranscriptPart): String =
        listOf(p.tool, p.text).filter { it.isNotEmpty() }.joinToString("  ") + if (p.failed) "  (failed)" else ""

    /** "12 earlier messages not shown", or null. */
    public fun omitted(n: Int): String? = if (n <= 0) null else "$n earlier message${if (n == 1) "" else "s"} not shown"
}
