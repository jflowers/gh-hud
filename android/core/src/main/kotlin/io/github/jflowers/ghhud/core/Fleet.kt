package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.FleetMachine
import io.github.jflowers.ghhud.api.v1.FleetSnapshot
import io.github.jflowers.ghhud.api.v1.MachineUsage
import io.github.jflowers.ghhud.api.v1.Snapshot

// The Fleet list, worked out from a hub snapshot: the TUI's F view of the
// GitOps rollout. A port of the Mac's Fleet.swift.

/** One machine in the rollout. */
public data class FleetRow(
    val name: String,
    val state: String,
    val tone: Tone,
    /** "yes", "no" or "unknown": whether the last probe reached it. */
    val reach: String,
    /** Agent slots in use, "1/2", or "—". */
    val slots: String,
    /** Slots in use, for sorting; -1 when unknown. */
    val slotsUsed: Int,
    /** The applied commit, "→ target" while it isn't current. */
    val applied: String,
    /** How long it has waited for agents to drain, in seconds. */
    val waited: Double?,
    val error: String,
) : ColumnFilterable {
    val id: String get() = name

    override val filterColumns: List<String> get() = COLUMNS

    override fun filterValues(column: String): List<String> = when (column) {
        "machine" -> listOf(name)
        "reached" -> listOf(reach)
        "state" -> listOf(state)
        else -> emptyList()
    }

    val stateRank: Int get() = tone.rank
    val waitedKey: Double get() = SortKey.duration(waited)

    public companion object {
        public val COLUMNS: List<String> = listOf("machine", "reached", "state")

        public fun of(m: FleetMachine, target: String, usage: List<MachineUsage>): FleetRow {
            val reach = when {
                m.state == "unreachable" || m.state == "skipped" -> "no"
                m.state == "pending" && !m.reachable -> "unknown"
                else -> if (m.reachable) "yes" else "unknown"
            }
            val u = usage.firstOrNull { it.name == m.name }
            val have = m.appliedShort.ifEmpty { "—" }
            val secs = m.waited.seconds + m.waited.nanos / 1e9
            return FleetRow(
                name = m.name,
                state = m.state,
                tone = FleetList.tone(m.state),
                reach = reach,
                slots = if (u != null && u.ok) "${u.used}/${u.max}" else "—",
                slotsUsed = if (u != null && u.ok) u.used.toInt() else -1,
                applied = if (m.state == "current" || target.isEmpty() || m.appliedShort == target) have else "$have → $target",
                waited = if (m.state == "draining" && secs > 0) secs else null,
                error = m.lastError.split(Regex("\\R")).filter { it.isNotEmpty() }.joinToString(" "),
            )
        }
    }
}

public object FleetList {
    public fun rows(s: Snapshot): List<FleetRow> =
        if (!s.hasFleet()) emptyList() else s.fleet.machinesList.map { FleetRow.of(it, s.fleet.targetShort, s.usageList) }

    /** How a machine's state reads. */
    public fun tone(state: String): Tone = when (state) {
        "current" -> Tone.GOOD
        "pending" -> Tone.MUTED
        "draining", "applying", "verifying" -> Tone.PENDING
        "failed" -> Tone.BAD
        else -> Tone.WARN // unreachable, skipped
    }

    /** How the target's CI reads. */
    public fun ciTone(ci: String): Tone = when (ci) {
        "green" -> Tone.GOOD
        "pending" -> Tone.PENDING
        "failed" -> Tone.BAD
        "no checks" -> Tone.WARN
        else -> Tone.MUTED
    }

    /** "2/3": machines on the target, of all. The tab's badge. */
    public fun progress(s: Snapshot): String {
        val ms = s.fleet.machinesList
        return "${ms.count { it.state == "current" }}/${ms.size}"
    }

    /**
     * The rollout in a few words: "failed on oak", "paused", "rolling
     * out", "blocked", or "up to date".
     */
    public fun state(f: FleetSnapshot): Pair<String, Tone> = when {
        f.failed.isNotEmpty() -> "failed on ${f.failed}" to Tone.BAD
        f.paused -> "paused" to Tone.WARN
        f.blocked -> "blocked" to Tone.WARN
        f.active -> "rolling out" to Tone.PENDING
        else -> "up to date" to Tone.GOOD
    }
}
