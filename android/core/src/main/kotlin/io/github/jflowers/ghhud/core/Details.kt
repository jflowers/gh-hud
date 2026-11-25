package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.CheckoutHealth
import io.github.jflowers.ghhud.api.v1.Run
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale

// What an agent's and a checkout's screens say, in the Mac's words
// (AgentDetail and WorkspaceDetail in macos/Sources/GHHud/AgentsPane.swift).

/** A labelled fact on a detail screen. */
public data class Fact(val label: String, val value: String)

/** A titled list of lines on a detail screen, with a note, in a tone. */
public data class FactList(val title: String, val note: String?, val lines: List<String>, val tone: Tone?)

public object AgentDetail {
    /** "Nov 15, 13:05  (5m ago)", in the phone's zone. */
    public fun whenText(t: Instant, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String =
        DateTimeFormatter.ofPattern("MMM d, HH:mm", Locale.US).withZone(zone).format(t) + "  (${ItemList.age(t, now)} ago)"

    /** The headline: the run's outcome in words, or the session's title. */
    public fun headline(r: AgentRow): Pair<String, Tone> = r.run?.let { RunTag.words(it.status) to RunTag.tone(it.status) }
        ?: (r.session.title.ifEmpty { r.session.id } to r.tone)

    public fun facts(r: AgentRow, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): List<Fact> {
        val run = r.run
        if (run == null) {
            val s = r.session
            return listOfNotNull(
                Fact("status", r.status.ifEmpty { "not running" }),
                Fact("dir", s.directory),
                Fact("session", s.id),
                Fact("updated", r.updated?.let { whenText(it, now, zone) } ?: ""),
                if (s.note.isEmpty()) null else Fact("note", s.note),
            )
        }
        return runFacts(run, r.took, now, zone)
    }

    internal fun runFacts(run: Run, took: Double?, now: Instant, zone: ZoneId): List<Fact> = listOfNotNull(
        Fact("run", "${run.task} ${run.ref}"),
        Fact("where", "${run.machine.ifEmpty { "local" }}:${run.workspace}  ${run.dir}"),
        Fact("started", whenText(run.startedAt.instant, now, zone)),
        took?.let { Fact("took", AgentList.duration(it)) },
        if (run.auto) Fact("trigger", "auto-dispatch") else null,
        if (run.sessionId.isNotEmpty()) Fact("session", run.sessionId) else null,
        if (run.branch.isNotEmpty()) Fact("branch", run.branch + if (run.cleaned) "  (returned to the default branch)" else "") else null,
    )

    /** What the run left: work behind, and what it pushed. */
    public fun lists(r: AgentRow): List<FactList> {
        val run = r.run ?: return emptyList()
        return listOfNotNull(
            if (run.leftCount > 0) FactList("Left behind", "commit, push or discard it, then Re-check", run.leftList, Tone.WARN) else null,
            if (run.pushedCount > 0) FactList("Pushed", null, run.pushedList, null) else null,
            RunFinding.text(run)?.let { (text, tone) -> FactList("Finding", null, listOf(text), tone) },
            DeweyIndex.run(run)?.let { (text, tone) ->
                val machine = text.startsWith("machine problem: ")
                FactList("Dewey index", if (machine) "a reindex here wouldn't help: fix the machine" else null, listOf(text), if (tone == Tone.GOOD) null else tone)
            },
        )
    }

    /** The run's final message, or why there's none; null while it runs. */
    public fun finalMessage(r: AgentRow): String? {
        val run = r.run ?: return null
        return when {
            run.summary.isNotEmpty() -> run.summary
            run.hasFinishedAt() -> "No final message recorded."
            else -> null
        }
    }
}

public object WorkspaceDetail {
    /** "Ready. A run here fast-forwards main to upstream." */
    public fun sentence(r: WorkspaceRow): String {
        val state = r.state.replaceFirstChar { it.uppercase() }
        return r.explanation?.let { "$state. $it" } ?: "$state."
    }

    public fun facts(r: WorkspaceRow): List<Fact> {
        val h = r.health
        return listOfNotNull(
            Fact("dir", h.dir),
            if (h.branch.isEmpty()) {
                null
            } else {
                val other = h.defaultBranch.isNotEmpty() && h.defaultBranch != h.branch
                Fact("branch", h.branch + if (other) "   (default ${h.defaultBranch})" else "")
            },
            if (h.behind >= 0) {
                Fact(
                    "upstream",
                    "${h.defaultBranch} is ${h.behind} behind, ${h.ahead} ahead of upstream/${h.defaultBranch} (as of the last fetch)",
                )
            } else {
                null
            },
            if (h.stashes > 0) Fact("stashes", "${h.stashes}") else null,
            if (h.learnings > 0) Fact("learnings", "${h.learnings} untracked; the next clean run opens the daily chore PR") else null,
        )
    }

    public fun lists(r: WorkspaceRow): List<FactList> {
        val h: CheckoutHealth = r.health
        return listOfNotNull(
            if (h.dirtyCount > 0) FactList("Uncommitted (${h.dirtyCount})", null, h.dirtyList, Tone.WARN) else null,
            if (h.localOnlyCount > 0) {
                FactList("Local-only commits: cleaning up loses them", null, h.localOnlyList.map(::localOnly), Tone.WARN)
            } else {
                null
            },
        )
    }

    /** "branch\tn" as "branch: n commits". */
    public fun localOnly(line: String): String {
        val parts = line.split("\t", limit = 2)
        val n = parts.getOrNull(1)?.toIntOrNull() ?: return line
        return "${parts[0]}: $n commit${if (n == 1) "" else "s"}"
    }
}

/** The agent list's orders: the Mac's column sorts. */
public enum class AgentSort(public val label: String, private val key: ((AgentRow) -> Comparable<*>)?) {
    HUB("Hub's order", null),
    STATUS("Status", { it.statusRank }),
    TOOK("Took", { it.tookKey }),
    AGE("Newest", { it.ageKey }),
    ;

    @Suppress("UNCHECKED_CAST")
    public fun sort(rows: List<AgentRow>, reversed: Boolean = false): List<AgentRow> {
        val k = key ?: return rows
        val c = compareBy<AgentRow> { k(it) as Comparable<Any> }
        return rows.sortedWith(if (reversed) c.reversed() else c)
    }
}

/** The checkout list's orders. */
public enum class WorkspaceSort(public val label: String, private val key: ((WorkspaceRow) -> Comparable<*>)?) {
    HUB("Hub's order", null),
    STATE("State", { it.stateRank }),
    SYNC("Behind upstream", { it.syncKey }),
    ;

    @Suppress("UNCHECKED_CAST")
    public fun sort(rows: List<WorkspaceRow>, reversed: Boolean = false): List<WorkspaceRow> {
        val k = key ?: return rows
        val c = compareBy<WorkspaceRow> { k(it) as Comparable<Any> }
        return rows.sortedWith(if (reversed) c.reversed() else c)
    }
}

public object AgentCommands {
    /** Reads a finished run's outcome again; the answer is what to say. */
    public suspend fun recheck(hub: HubConnection, r: AgentRow): Answer<String> {
        val run = r.run ?: return Answer.No("No run to re-check")
        return when (val a = hub.reinspect(run.id)) {
            is Answer.No -> Answer.No("Can't re-check ${run.task} on ${run.ref}: ${a.why}")
            is Answer.Ok -> Answer.Ok("Re-checked ${run.task} on ${run.ref}: ${ItemCommands.outcome(a.value)}")
        }
    }
}
