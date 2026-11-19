package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.CheckoutHealth
import io.github.jflowers.ghhud.api.v1.DeweyPass
import io.github.jflowers.ghhud.api.v1.FleetMachine
import io.github.jflowers.ghhud.api.v1.FleetSnapshot
import io.github.jflowers.ghhud.api.v1.ReindexResult
import io.github.jflowers.ghhud.api.v1.Run
import java.time.Instant
import java.time.ZoneId

// The Dewey index (#139): each checkout's index health on the Workspaces
// tab and its screen, a run's after-run check, and the fleet's reindex
// passes. In the TUI's words (internal/ui/workspaces_index.go, runs.go and
// the fleet view in view.go). A reindex need doesn't block a dispatch.

/** A line under a fleet machine, in a tone. */
public data class ToneLine(val text: String, val tone: Tone)

public object DeweyIndex {
    /** CheckoutHealth.index for a checkout that isn't a Dewey vault. */
    public const val NONE: String = "none"

    /** CheckoutHealth.index while dewey status hasn't been read since the index was built. */
    public const val UNCHECKED: String = "unchecked"

    /** The parts of CheckoutHealth.index: reindex reasons, a machine problem, a failed check. */
    public data class Parts(val reasons: List<String>, val machine: String, val failed: String)

    /**
     * Splits an index verdict ("reindex needed: a, b; machine problem: …")
     * into its parts. Prefer [reasons] for the reasons: Dewey's own contain
     * commas.
     */
    public fun parts(index: String): Parts {
        var reasons = emptyList<String>()
        var machine = ""
        var failed = ""
        for (p in index.split("; ")) {
            when {
                p.startsWith(REINDEX) -> reasons = p.removePrefix(REINDEX).split(", ")
                p.startsWith(MACHINE) -> machine = p.removePrefix(MACHINE)
                p.startsWith(FAILED) -> failed = p.removePrefix(FAILED)
            }
        }
        return Parts(reasons, machine, failed)
    }

    /** The checkout's reindex reasons: index_reasons, else split out of index (an older hub). */
    public fun reasons(h: CheckoutHealth): List<String> {
        val split = parts(h.index).reasons
        return if (h.indexReasonsCount > 0 && split.isNotEmpty()) h.indexReasonsList else split
    }

    /**
     * A reason as the list names it: gh-hud's own as they are, Dewey's
     * (sentences like "index format 0, this Dewey writes 1") by what
     * they're about.
     */
    public fun short(reason: String): String = when {
        reason == "unindexed" || reason == "no embeddings" -> reason
        reason.startsWith("index format") -> "old format"
        else -> "dewey: reindex"
    }

    /** The share of blocks with an embedding ("100%"); null before dewey status is read or for an empty index. */
    public fun coverage(h: CheckoutHealth): String? =
        if (!h.hasIndexStatusAt() || h.indexBlocks <= 0) null else "${h.indexEmbeddings * 100 / h.indexBlocks}%"

    /**
     * The index in a few words for the checkout's row: "ok 2d 100%", "old
     * format 3d", "unindexed", "no embeddings" (the machine's fault, so a
     * reindex won't help), "reindexing…". Null when there is nothing to
     * say: not read, or not a Dewey vault.
     */
    public fun state(h: CheckoutHealth, now: Instant = Instant.now()): Pair<String, Tone>? {
        if (h.reindexing) return "reindexing…" to Tone.PENDING
        if (h.index.isEmpty() || h.index == NONE) return null
        val p = parts(h.index)
        val reasons = reasons(h)
        var (text, tone) = when {
            reasons.isNotEmpty() ->
                (short(reasons[0]) + if (reasons.size > 1) " +${reasons.size - 1}" else "") to Tone.WARN
            p.machine.isNotEmpty() -> when {
                "no dewey" in p.machine -> "no dewey"
                "is older than" in p.machine -> "dewey too old"
                else -> "no embeddings"
            } to Tone.WARN
            p.failed.isNotEmpty() -> "check failed" to Tone.BAD
            h.index == UNCHECKED -> UNCHECKED to Tone.MUTED
            else -> "ok" to Tone.GOOD
        }
        val extra = listOfNotNull(if (h.hasIndexBuilt()) ItemList.age(h.indexBuilt.instant, now) else null, coverage(h))
        if (extra.isNotEmpty()) text += " " + extra.joinToString(" ")
        return text to tone
    }

    /** What an agent dispatched here works with, for a reason. */
    public fun means(reason: String): String = when (reason) {
        "unindexed" -> "no index: the agent's first search builds one mid-run"
        "no embeddings" -> "keyword search only: semantic search finds nothing"
        else -> "Dewey says to rebuild it: search may miss what this Dewey would index"
    }

    /**
     * The verdict for the checkout's screen, its reasons summed up (they
     * are listed one per line): "reindex needed (still dispatchable)",
     * "ok", "machine problem: …". Null when not read.
     */
    public fun verdict(h: CheckoutHealth): String? = when (h.index) {
        "" -> null
        NONE -> "none (not a Dewey vault)"
        else -> {
            val v = h.index.split("; ").joinToString("; ") { if (it.startsWith(REINDEX)) "reindex needed" else it }
            if (reasons(h).isNotEmpty()) "$v (still dispatchable)" else v
        }
    }

    /** The index facts on a checkout's screen: verdict, who built it and when, what it held, the last reindex. */
    public fun facts(h: CheckoutHealth, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): List<Fact> {
        val v = verdict(h)
        val out = mutableListOf<Fact>()
        if (v != null) out += Fact("index", v)
        if (h.reindexing) out += Fact("dewey", "reindexing the index; no dispatch here until it ends")
        if (v != null && h.index != NONE) {
            if (h.hasIndexBuilt()) {
                var built = AgentDetail.whenText(h.indexBuilt.instant, now, zone)
                if (h.hasIndexStatusAt()) {
                    built += if (h.indexDeweyVersion.isEmpty() && h.indexFormat == 0L) {
                        ", by a Dewey before 3.9.0 (format 0)"
                    } else {
                        ", " + builtBy(h.indexDeweyVersion, h.indexFormat)
                    }
                }
                out += Fact("built", built)
            }
            when {
                h.hasIndexStatusAt() -> {
                    var c = "${h.indexPages} pages · ${h.indexBlocks} blocks · ${h.indexEmbeddings} embedded"
                    coverage(h)?.let { c += " ($it)" }
                    out += Fact("contents", "$c, as of dewey status ${ago(h.indexStatusAt.instant, now)}")
                }
                h.hasIndexBuilt() -> out += Fact("contents", "dewey status not read yet")
            }
        }
        if (h.hasLastReindex()) out += Fact("last reindex", reindexLine(h.lastReindex, now, zone))
        return out
    }

    /** The index lists on a checkout's screen: each reason on its own line, a machine problem, a failed check. */
    public fun lists(h: CheckoutHealth): List<FactList> {
        if (h.index.isEmpty() || h.index == NONE) return emptyList()
        val p = parts(h.index)
        val reasons = reasons(h)
        return listOfNotNull(
            if (reasons.isEmpty()) {
                null
            } else {
                FactList("Reindex needed", "what an agent here works with until then", reasons.flatMap { listOf(it, "  → " + means(it)) }, Tone.WARN)
            },
            if (p.machine.isEmpty()) null else FactList("Machine problem", "a reindex here wouldn't help: fix the machine", listOf(p.machine), Tone.WARN),
            if (p.failed.isEmpty()) null else FactList("Check failed", null, listOf(p.failed), Tone.BAD),
        )
    }

    /** "format 1 by 3.9.0". */
    public fun builtBy(version: String, format: Long): String = "format $format" + if (version.isEmpty()) "" else " by $version"

    /**
     * A reindex's outcome: "ok in 3m12s, Nov 18, 09:30  (2h ago), for index
     * format 0, this Dewey writes 1; now format 1 by 3.9.0".
     */
    public fun reindexLine(r: ReindexResult, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
        val secs = r.duration.seconds + if (r.duration.nanos >= 500_000_000) 1 else 0
        var s = when {
            r.skipped.isNotEmpty() -> "skipped: ${r.skipped}"
            r.error.isNotEmpty() -> "failed after ${goDuration(secs)}: ${r.error}"
            else -> "ok in ${goDuration(secs)}"
        }
        if (r.hasAt()) s += ", " + AgentDetail.whenText(r.at.instant, now, zone)
        if (r.reasonsCount > 0) s += ", for " + r.reasonsList.joinToString(", ")
        if (r.deweyVersion.isNotEmpty() || r.indexFormat > 0) s += "; now " + builtBy(r.deweyVersion, r.indexFormat)
        return s
    }

    /** Whether the last reindex failed, and nothing reindexes now: the row's notes say so. */
    public fun reindexFailed(h: CheckoutHealth): Boolean = h.hasLastReindex() && h.lastReindex.error.isNotEmpty() && !h.reindexing

    /** A run's after-run index check: "ok" good, anything else a warning; null when not checked. */
    public fun run(r: Run): Pair<String, Tone>? = when (r.indexHealth) {
        "" -> null
        "ok" -> "ok" to Tone.GOOD
        else -> r.indexHealth to Tone.WARN
    }

    /** Seconds as Go prints a whole-second duration: "45s", "3m12s", "1h2m0s". */
    public fun goDuration(secs: Long): String = when {
        secs < 60 -> "${secs}s"
        secs < 3600 -> "${secs / 60}m${secs % 60}s"
        else -> "${secs / 3600}h${secs % 3600 / 60}m${secs % 60}s"
    }

    private fun ago(t: Instant, now: Instant): String = ItemList.age(t, now).let { if (it == "now") "just now" else "$it ago" }

    private const val REINDEX = "reindex needed: "
    private const val MACHINE = "machine problem: "
    private const val FAILED = "check failed: "
}

/** A fleet machine's Dewey reindexes: after a rollout changed Dewey, and the pass you asked for. */
public object DeweyFleet {
    /** The failed or skipped vaults listed under a machine; the rest are counted. */
    public const val MAX_LISTED: Int = 5

    /** "reindexing 7/15" while a rollout's reindex runs, else the state as it is. */
    public fun stateText(m: FleetMachine): String =
        if (m.state == "reindexing" && m.reindexTotal > 0) "reindexing ${m.reindexDone}/${m.reindexTotal}" else m.state

    /** The rollout's reindex: its summary, then each vault that failed. */
    public fun rolloutLines(m: FleetMachine): List<ToneLine> {
        if (m.reindexNote.isEmpty() && m.reindexFailuresCount == 0) return emptyList()
        val out = mutableListOf<ToneLine>()
        if (m.reindexNote.isNotEmpty()) out += ToneLine("dewey: " + m.reindexNote, Tone.MUTED)
        m.reindexFailuresList.forEachIndexed { i, f ->
            if (i == MAX_LISTED) {
                out += ToneLine("✗ ${m.reindexFailuresCount - i} more", Tone.BAD)
                return out
            }
            out += ToneLine("✗ " + oneLine(f), Tone.BAD)
        }
        return out
    }

    /** "reindexing 3/12" while the pass runs, then its summary. */
    public fun passText(p: DeweyPass): String = if (p.running && p.total > 0) "reindexing ${p.done}/${p.total}" else p.note

    /**
     * The pass you asked for: "dewey pass: reindexing 3/12 · started 5m
     * ago", or its summary, then each vault that failed (✗) or was skipped
     * (–). Empty when none was asked for.
     */
    public fun passLines(m: FleetMachine, now: Instant = Instant.now()): List<ToneLine> {
        if (!m.hasDeweyPass() || !m.deweyPass.hasStarted()) return emptyList()
        val p = m.deweyPass
        var head = "dewey pass: " + passText(p)
        if (p.forced) head += " · forced"
        if (p.running) {
            head += " · started " + AgentList.duration(secondsBetween(p.started.instant, now).coerceAtLeast(0.0)) + " ago"
        } else if (p.hasEnded()) {
            head += " · " + AgentList.duration(secondsBetween(p.ended.instant, now).coerceAtLeast(0.0)) + " ago"
        }
        val tone = when {
            p.running -> Tone.PENDING
            p.failedCount > 0 -> Tone.BAD
            p.skippedCount > 0 -> Tone.WARN
            else -> Tone.MUTED
        }
        val out = mutableListOf(ToneLine(head, tone))
        val all = p.failedList.map { "✗ " + oneLine(it) } + p.skippedList.map { "– " + oneLine(it) }
        all.forEachIndexed { i, l ->
            if (i == MAX_LISTED) {
                out += ToneLine("${all.size - i} more", Tone.BAD)
                return out
            }
            out += ToneLine(l, if (l.startsWith("✗")) Tone.BAD else Tone.WARN)
        }
        return out
    }

    private fun oneLine(s: String): String = s.replace(Regex("\\R"), " ")
}

/**
 * "Reindex the vaults now" (the TUI's d, D, f and x in the fleet view):
 * a pass on a machine, or on every one (machine null), that reindexes
 * the vaults whose check says so, or every vault when forced. Busy
 * vaults are skipped. The hub may refuse; its words are passed on.
 */
public object DeweyCommands {
    /** Whether a pass runs on the machine, or on any (machine null). */
    public fun running(f: FleetSnapshot, machine: String? = null): Boolean =
        f.machinesList.any { (machine == null || it.name == machine) && it.hasDeweyPass() && it.deweyPass.running }

    /** The confirmation's title. */
    public fun ask(machine: String?, force: Boolean): String {
        val where = machine ?: "every machine"
        return if (force) "Reindex every Dewey vault on $where?" else "Reindex the Dewey vaults that need it on $where?"
    }

    /** What the confirmation says the pass does. */
    public fun explain(force: Boolean): String = (
        if (force) {
            "Every vault is reindexed, whatever its index check says."
        } else {
            "The vaults whose index check (Dewey's own verdict) says so are reindexed."
        }
        ) + " Busy vaults are skipped, and a machine reindexes one vault at a time; agents can't start in a vault while it does."

    public suspend fun start(hub: HubConnection, machine: String?, force: Boolean): Answer<String> =
        when (val a = hub.deweyReindex(machine.orEmpty(), all = machine == null, force = force)) {
            is Answer.No -> Answer.No("Dewey: ${a.why}")
            is Answer.Ok -> {
                var said = if (a.value.machinesCount == 0) {
                    "No Dewey reindex pass started"
                } else {
                    (if (force) "Forced Dewey" else "Dewey") + " reindex pass started on " + a.value.machinesList.joinToString(", ")
                }
                if (a.value.refusedCount > 0) said += "; not on " + a.value.refusedList.joinToString("; ")
                Answer.Ok(said)
            }
        }

    public suspend fun cancel(hub: HubConnection, machine: String?): Answer<String> =
        when (val a = hub.deweyReindex(machine.orEmpty(), all = machine == null, cancel = true)) {
            is Answer.No -> Answer.No("Dewey: ${a.why}")
            is Answer.Ok -> Answer.Ok(
                if (a.value.machinesCount == 0) {
                    "No Dewey reindex pass to cancel"
                } else {
                    "Dewey reindex pass cancelled on " + a.value.machinesList.joinToString(", ")
                },
            )
        }
}
