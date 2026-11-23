package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.CheckoutHealth
import io.github.jflowers.ghhud.api.v1.Item
import io.github.jflowers.ghhud.api.v1.Run
import io.github.jflowers.ghhud.api.v1.Session
import io.github.jflowers.ghhud.api.v1.Snapshot
import java.time.Instant

// The Agents and Workspaces lists, worked out from a hub snapshot: the
// TUI's tabs 6 and 9. A port of the Mac's Agents.swift.

/** One agent session, with the dispatch run behind it when there is one. */
public data class AgentRow(
    /**
     * The run's ID for a dispatch, else the session's. Session IDs repeat:
     * dispatching the same task on the same item and machine again reuses
     * the name, so two rows (the new run and the old) share it.
     */
    val id: String,
    val session: Session,
    val run: Run?,
    /** The task, or "" for a session gh-hud didn't start. */
    val task: String,
    /** The ref and the item's title for a dispatch; the session title otherwise. */
    val name: String,
    val machine: String, // "local" for the hub's machine
    val workspace: String,
    /** "running", "blocked", "failed", "left work"…; "" when idle. */
    val status: String,
    val tone: Tone,
    /** How long the run took (or has taken so far), in seconds. */
    val took: Double?,
    val updated: Instant?,
    /** Live or a dispatch: shown without "Show stale". */
    val recent: Boolean,
) : ColumnFilterable {
    /**
     * Executing now: a running or blocked run, or a live session gh-hud
     * didn't start, whatever state it reports. "Live only" shows these.
     */
    val live: Boolean get() = run?.let { it.status == "running" || it.status == "blocked" } ?: (session.running || session.blocked)

    override val filterColumns: List<String> get() = COLUMNS

    override fun filterValues(column: String): List<String> = when (column) {
        "task" -> if (task.isEmpty()) emptyList() else listOf(task)
        "agent" -> listOf(name)
        "where" -> listOf(machine)
        "workspace" -> if (workspace.isEmpty()) emptyList() else listOf(workspace)
        "status" -> if (status.isEmpty()) emptyList() else listOf(status)
        else -> emptyList()
    }

    val statusRank: Int get() = if (status.isEmpty()) Tone.MUTED.rank + 1 else tone.rank
    val tookKey: Double get() = SortKey.duration(took)
    val ageKey: Double get() = SortKey.age(updated)

    public companion object {
        public val COLUMNS: List<String> = listOf("task", "agent", "where", "workspace", "status")

        public fun of(s: Session, run: Run?, items: List<Item>, now: Instant = Instant.now()): AgentRow {
            val name = when {
                s.ref.isEmpty() -> s.title.ifEmpty { s.id }
                else -> items.firstOrNull { it.ref == s.ref }?.let { "${s.ref}  ${it.title}" } ?: s.ref
            }
            val updated = if (s.hasUpdatedAt()) s.updatedAt.instant else null
            var took: Double? = null
            val (status, tone) = when {
                run != null -> {
                    val end = if (run.hasFinishedAt()) run.finishedAt.instant else now
                    took = if (run.hasStartedAt()) maxOf(0.0, secondsBetween(run.startedAt.instant, end)) else null
                    (if (run.status == "left-work") "left work" else run.status) to RunTag.tone(run.status)
                }
                s.blocked -> "blocked" to Tone.WARN
                s.running -> s.state.ifEmpty { "running" } to Tone.PENDING
                s.failed -> "failed" to Tone.BAD
                s.unknown -> "unknown" to Tone.WARN
                else -> "" to Tone.MUTED
            }
            val cutoff = now.minusSeconds(AgentList.RECENT_WINDOW)
            return AgentRow(
                id = s.runId.ifEmpty { s.id },
                session = s,
                run = run,
                task = s.task,
                name = name,
                machine = s.machine.ifEmpty { "local" },
                workspace = s.workspace,
                status = status,
                tone = tone,
                took = took,
                updated = updated,
                recent = s.task.isNotEmpty() || s.running || s.blocked || s.failed || (updated?.isAfter(cutoff) ?: false),
            )
        }
    }
}

public object AgentList {
    /** Sessions idle longer than this (seconds), and not dispatches, are stale. */
    public const val RECENT_WINDOW: Long = 86400

    /**
     * The sessions in the hub's order; stale ones only when asked, and with
     * liveOnly only the ones executing now.
     */
    public fun rows(s: Snapshot, showStale: Boolean, liveOnly: Boolean = false, now: Instant = Instant.now()): List<AgentRow> {
        val runs = LinkedHashMap<String, Run>()
        for (r in s.runsList) runs.putIfAbsent(r.id, r)
        val seen = HashMap<String, Int>()
        return s.sessionsList
            .map { AgentRow.of(it, if (it.runId.isEmpty()) null else runs[it.runId], s.result.itemsList, now) }
            .filter { (showStale || it.recent) && (!liveOnly || it.live) }
            .map { r ->
                // Every row's ID must be unique; a repeat (sessions without
                // a run) gets a suffix.
                val n = seen[r.id] ?: 0
                seen[r.id] = n + 1
                if (n == 0) r else r.copy(id = "${r.id}#$n")
            }
    }

    /** Live sessions: the count on the Agents tab. */
    public fun running(s: Snapshot): Int = s.sessionsList.count { it.running }

    /** "45s", "12m", "1h05". */
    public fun duration(t: Double): String {
        val secs = t.toInt()
        return when {
            secs < 60 -> "${secs}s"
            secs < 3600 -> "${secs / 60}m"
            else -> "%dh%02d".format(secs / 3600, (secs % 3600) / 60)
        }
    }
}

/** One checkout a dispatch could use. */
public data class WorkspaceRow(
    val id: String, // machine and directory
    val health: CheckoutHealth,
    /** "chai:widgets-2", or the workspace alone on the hub's machine. */
    val place: String,
    val machine: String, // "local" for the hub's machine
    val repo: String, // the name, without the owner
    /** The default branch against upstream: "↓3 ↑1", "=", or "—" unknown. */
    val sync: String,
    /** "ready", "running #12", "2 uncommitted"… */
    val state: String,
    val tone: Tone,
    /** Not blocking but worth knowing: "2 local-only · 1 stash". */
    val notes: String,
    /** What the state means for the next run, when there's more to say. */
    val explanation: String?,
) : ColumnFilterable {
    val ready: Boolean get() = state == "ready"
    val workspace: String get() = health.workspace
    val branch: String get() = health.branch

    override val filterColumns: List<String> get() = COLUMNS

    override fun filterValues(column: String): List<String> = when (column) {
        "where" -> listOf(machine)
        "workspace" -> listOf(workspace)
        "repo" -> listOf(repo)
        "branch" -> if (branch.isEmpty()) emptyList() else listOf(branch)
        "state" -> listOf(state)
        else -> emptyList()
    }

    val stateRank: Int get() = tone.rank

    /** Furthest behind upstream first when descending; unknown last. */
    val syncKey: Long get() =
        if (health.behind < 0 || health.ahead < 0) Long.MAX_VALUE else health.behind * 1000 + health.ahead

    public companion object {
        public val COLUMNS: List<String> = listOf("where", "workspace", "repo", "branch", "state")

        public fun of(h: CheckoutHealth): WorkspaceRow {
            val sync = if (h.behind < 0 || h.ahead < 0) {
                "—"
            } else {
                listOfNotNull(if (h.behind > 0) "↓${h.behind}" else null, if (h.ahead > 0) "↑${h.ahead}" else null)
                    .joinToString(" ").ifEmpty { "=" }
            }
            val problem = problem(h)
            val (state, tone) = when {
                h.error.isNotEmpty() -> h.error to Tone.BAD
                h.busy.isNotEmpty() -> {
                    val i = h.busy.lastIndexOf('#')
                    "running ${if (i >= 0) h.busy.substring(i) else h.busy}" to Tone.PENDING
                }
                h.reindexing || h.upkeeping -> problem to Tone.PENDING
                h.noUpstream -> problem to Tone.WARN
                problem.isNotEmpty() -> problem to Tone.BAD
                else -> "ready" to Tone.GOOD
            }
            val notes = buildList {
                val local = localOnlyCommits(h)
                if (local > 0) add("$local local-only")
                if (h.stashes > 0) add("${h.stashes} stash")
                if (h.learnings > 0) add("${h.learnings} learnings")
                if (DeweyIndex.reindexFailed(h)) add("reindex failed")
            }.joinToString(" · ")
            val ready = problem.isEmpty()
            val explanation = when {
                ready && h.defaultBranch.isNotEmpty() && h.branch != h.defaultBranch ->
                    "A run here switches to ${h.defaultBranch} and fast-forwards it."
                ready -> "A run here fast-forwards ${h.defaultBranch} to upstream."
                h.reindexing -> "gh-hud is reindexing its Dewey index: no run starts here until it ends."
                h.upkeeping -> "gh-hud is fetching and fast-forwarding it: no run starts here until it ends."
                h.dirtyCount > 0 && h.busy.isEmpty() -> "A run here is refused until these are committed or discarded."
                h.ahead > 0 && h.behind > 0 -> "${h.defaultBranch} can't fast-forward to upstream/${h.defaultBranch}."
                else -> null
            }
            return WorkspaceRow(
                id = h.machine + "\u0000" + h.dir,
                health = h,
                place = if (h.machine.isEmpty()) h.workspace else "${h.machine}:${h.workspace}",
                machine = h.machine.ifEmpty { "local" },
                repo = h.repo.split("/", limit = 2).last(),
                sync = sync,
                state = state,
                tone = tone,
                notes = notes,
                explanation = explanation,
            )
        }

        /** Why the next run can't start here; "" when it can. */
        internal fun problem(h: CheckoutHealth): String = when {
            h.error.isNotEmpty() -> h.error
            h.busy.isNotEmpty() -> "running " + h.busy
            h.reindexing -> "reindexing"
            h.upkeeping -> "upkeep"
            h.noUpstream -> "no upstream remote"
            h.dirtyCount > 0 -> "${h.dirtyCount} uncommitted"
            h.ahead > 0 && h.behind > 0 -> h.defaultBranch + " diverged"
            else -> ""
        }

        /** Commits on no remote, across branches ("branch\tcount" lines). */
        internal fun localOnlyCommits(h: CheckoutHealth): Int =
            h.localOnlyList.sumOf { l -> l.split("\t", limit = 2).last().toIntOrNull() ?: 0 }
    }
}

public object WorkspaceList {
    /** The checkouts; a worker's unmanaged-removal row isn't one. */
    public fun rows(s: Snapshot): List<WorkspaceRow> = s.workspacesList.filter { !it.unmanaged }.map { WorkspaceRow.of(it) }

    /**
     * The checkout an alert names: "[machine:]workspace", or with "/repo"
     * after it (upkeep's). Null when it is no longer in the list.
     */
    public fun byRef(s: Snapshot, ref: String): WorkspaceRow? {
        val rows = rows(s)
        rows.firstOrNull { it.place == ref }?.let { return it }
        val i = ref.lastIndexOf('/')
        if (i < 0) return null
        val place = ref.substring(0, i)
        val repo = ref.substring(i + 1)
        return rows.firstOrNull { it.place == place && (it.repo == repo || it.health.repo.endsWith("/$repo")) }
            ?: rows.firstOrNull { it.place == place }
    }

    /** Checkouts the next run could use: the count on the tab. */
    public fun ready(s: Snapshot): Int = rows(s).count { it.ready }

    /**
     * Whether the workspaces are worth reading again on opening the list
     * (the TUI's 30 seconds).
     */
    public fun stale(s: Snapshot, now: Instant = Instant.now()): Boolean =
        !s.workspacesLoading && (!s.hasWorkspacesAt() || secondsBetween(s.workspacesAt.instant, now) > 30)
}
