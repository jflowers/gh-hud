package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.FleetAction
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
    /** The state as shown: "reindexing 7/15" while a rollout reindexes Dewey. */
    val stateText: String,
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
    /** What pruning unused tool versions did after the last verified apply ("" before one ran). */
    val pruned: String = "",
    val prunedTone: Tone = Tone.MUTED,
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
                stateText = DeweyFleet.stateText(m),
                tone = FleetList.tone(m.state),
                reach = reach,
                slots = if (u != null && u.ok) "${u.used}/${u.max}" else "—",
                slotsUsed = if (u != null && u.ok) u.used.toInt() else -1,
                applied = if (m.state == "current" || target.isEmpty() || m.appliedShort == target) have else "$have → $target",
                waited = if (m.state == "draining" && secs > 0) secs else null,
                error = m.lastError.split(Regex("\\R")).filter { it.isNotEmpty() }.joinToString(" "),
                pruned = m.pruneNote,
                prunedTone = FleetList.pruneTone(m.pruneNote),
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
        "draining", "applying", "reindexing", "verifying" -> Tone.PENDING
        "failed" -> Tone.BAD
        else -> Tone.WARN // unreachable, skipped
    }

    /** How a prune note reads: bad when the prune failed. */
    public fun pruneTone(note: String): Tone = if (note.contains("failed")) Tone.BAD else Tone.MUTED

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

/** The fleet list's orders: the Mac's column sorts. */
public enum class FleetSort(public val label: String, private val key: ((FleetRow) -> Comparable<*>)?) {
    HUB("Hub's order", null),
    STATE("State", { it.stateRank }),
    WAITED("Waited", { it.waitedKey }),
    SLOTS("Slots in use", { it.slotsUsed }),
    ;

    @Suppress("UNCHECKED_CAST")
    public fun sort(rows: List<FleetRow>, reversed: Boolean = false): List<FleetRow> {
        val k = key ?: return rows
        val c = compareBy<FleetRow> { k(it) as Comparable<Any> }
        return rows.sortedWith(if (reversed) c.reversed() else c)
    }
}

/**
 * The rollout's controls, the TUI's p, a, s and r, and how each went in
 * the Mac's words (macos/Sources/GHHud/FleetPane.swift).
 */
public object FleetCommands {
    /** Why the controls are off: no hub, or no fleet. */
    public fun off(state: HubState): String? = when {
        state.status != HubStatus.Connected -> "Not connected to the hub"
        !state.snapshot.hasFleet() -> "The fleet is off on the hub"
        else -> null
    }

    /** Skip wait applies only while a machine drains. */
    public fun canSkipWait(f: FleetSnapshot): Boolean = f.machinesList.any { it.state == "draining" }

    /** Retry applies only once a machine failed. */
    public fun canRetry(f: FleetSnapshot): Boolean = f.failed.isNotEmpty()

    public suspend fun run(hub: HubConnection, action: FleetAction, f: FleetSnapshot): Answer<String> {
        val said = when (action) {
            FleetAction.FLEET_ACTION_PAUSE -> "Paused the rollout"
            FleetAction.FLEET_ACTION_RESUME -> "Resumed the rollout"
            FleetAction.FLEET_ACTION_APPLY_NOW -> "Checking the target now"
            FleetAction.FLEET_ACTION_SKIP_WAIT -> "Applying without waiting for agents to finish"
            FleetAction.FLEET_ACTION_RETRY -> "Retrying ${f.failed}"
            else -> return Answer.No("Fleet: no such control")
        }
        return when (val a = hub.fleet(action)) {
            is Answer.Ok -> Answer.Ok(said)
            is Answer.No -> Answer.No("Fleet: ${a.why}")
        }
    }
}
