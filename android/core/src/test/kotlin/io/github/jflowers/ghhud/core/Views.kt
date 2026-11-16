package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.Snapshot
import java.time.Instant

/**
 * Everything the views make of a snapshot, as the text in
 * testdata/clients/<name>.views.txt. The Mac's checks render the same text
 * (macos/Sources/GHHudChecks/Views.swift); README.md there says how.
 */
object Views {
    /** The searches every fixture is rendered with. */
    val ITEM_SEARCHES = listOf("gizmo", "repo:widgets -author:dependabot", "label:\"good first\"", "status:\"ci failing\" kind:mine")
    val AGENT_SEARCHES = listOf("where:chai", "-status:failed fix")

    fun render(s: Snapshot): String {
        val now = s.fetchedAt.instant
        val out = Out()

        val counts = ItemList.counts(s)
        for (f in ItemFilter.entries) out["items.count.${f.id}"] = counts[f]
        out["items.commandsOff"] = ItemList.commandsOff(s)
        val items = ItemList.rows(s, ItemFilter.ALL, now = now)
        items.forEachIndexed { i, r -> item(out, "items.all[$i]", r, now) }
        sorts(out, "items", items, { it.id }, "markerRank" to { it.markerRank }, "statusRank" to { it.statusRank },
            "numKey" to { it.numKey }, "threadsKey" to { it.threadsKey }, "ageKey" to { it.ageKey })
        for (f in ItemFilter.entries.drop(1)) {
            out["items.${f.id}"] = ItemList.rows(s, f, now = now).map { it.id }
        }
        for (q in ITEM_SEARCHES) out["items.search[${quote(q)}]"] = ItemList.rows(s, ItemFilter.ALL, q, now = now).map { it.id }

        out["agents.running"] = AgentList.running(s)
        out["agents.recent"] = AgentList.rows(s, showStale = false, now = now).map { it.id }
        out["agents.live"] = AgentList.rows(s, showStale = false, liveOnly = true, now = now).map { it.id }
        val agents = AgentList.rows(s, showStale = true, now = now)
        agents.forEachIndexed { i, r ->
            val p = "agents.all[$i]"
            out[p] = r.id
            out["$p.task"] = r.task
            out["$p.name"] = r.name
            out["$p.machine"] = r.machine
            out["$p.workspace"] = r.workspace
            out["$p.status"] = r.status
            out["$p.tone"] = tone(r.tone)
            out["$p.statusRank"] = r.statusRank
            out["$p.took"] = r.took?.let { AgentList.duration(it) }
            out["$p.updated"] = epoch(r.updated)
            out["$p.recent"] = r.recent
            out["$p.live"] = r.live
        }
        sorts(out, "agents", agents, { it.id }, "statusRank" to { it.statusRank }, "tookKey" to { it.tookKey },
            "ageKey" to { it.ageKey })
        for (q in AGENT_SEARCHES) {
            out["agents.search[${quote(q)}]"] = TableFilter.rows(agents, AgentRow.COLUMNS, q, emptyList()).map { it.id }
        }

        out["workspaces.ready"] = WorkspaceList.ready(s)
        out["workspaces.stale"] = WorkspaceList.stale(s, now)
        WorkspaceList.rows(s).forEachIndexed { i, r ->
            val p = "workspaces[$i]"
            out[p] = r.place
            out["$p.machine"] = r.machine
            out["$p.repo"] = r.repo
            out["$p.sync"] = r.sync
            out["$p.syncKey"] = r.syncKey
            out["$p.state"] = r.state
            out["$p.tone"] = tone(r.tone)
            out["$p.notes"] = r.notes
            out["$p.explanation"] = r.explanation
            out["$p.ready"] = r.ready
        }

        sorts(out, "workspaces", WorkspaceList.rows(s), { it.place }, "stateRank" to { it.stateRank }, "syncKey" to { it.syncKey })

        if (s.hasFleet()) {
            val (state, t) = FleetList.state(s.fleet)
            out["fleet.state"] = state
            out["fleet.tone"] = tone(t)
            out["fleet.ci"] = tone(FleetList.ciTone(s.fleet.ci))
            out["fleet.progress"] = FleetList.progress(s)
        }
        FleetList.rows(s).forEachIndexed { i, r ->
            val p = "fleet[$i]"
            out[p] = r.name
            out["$p.state"] = r.state
            out["$p.tone"] = tone(r.tone)
            out["$p.reach"] = r.reach
            out["$p.slots"] = r.slots
            out["$p.slotsUsed"] = r.slotsUsed
            out["$p.applied"] = r.applied
            out["$p.waited"] = r.waited?.let { AgentList.duration(it) }
            out["$p.error"] = r.error
        }

        sorts(out, "fleet", FleetList.rows(s), { it.name }, "stateRank" to { it.stateRank },
            "waitedKey" to { it.waitedKey }, "slotsUsed" to { it.slotsUsed })

        out["plan.summary"] = PlanList.summary(s)
        out["plan.starting"] = PlanList.starting(s)
        PlanList.groups(s).forEachIndexed { g, group ->
            out["plan[$g]"] = group.title
            group.rows.forEachIndexed { i, r ->
                val p = "plan[$g][$i]"
                out[p] = r.id
                out["$p.position"] = r.position
                out["$p.title"] = r.title
                out["$p.url"] = r.url
                out["$p.status"] = r.status
                out["$p.tone"] = tone(r.tone)
                out["$p.quiet"] = r.quiet
                out["$p.queued"] = r.queued
                out["$p.missing"] = r.missing
                out["$p.epic"] = r.epic
                out["$p.gated"] = r.gated
            }
        }
        PlanList.cases(s).forEachIndexed { i, c ->
            val p = "cases[$i]"
            out[p] = c.id
            out["$p.title"] = c.title
            out["$p.scope"] = c.scope
            out["$p.status"] = c.status
            out["$p.tone"] = tone(c.tone)
            out["$p.summary"] = c.summary
            out["$p.proposal"] = c.proposal
        }
        PlanList.epics(s).forEachIndexed { i, e ->
            val p = "epics[$i]"
            out[p] = e.ref
            out["$p.title"] = e.title
            out["$p.url"] = e.url
            out["$p.status"] = e.status
            out["$p.tone"] = tone(e.tone)
            out["$p.planToApprove"] = e.planToApprove
            e.changes.forEachIndexed { j, c -> out["$p.changes[$j]"] = c }
            out["$p.done"] = e.done
            out["$p.total"] = e.total
            e.open.forEachIndexed { j, n ->
                val q = "$p.open[$j]"
                out[q] = n.ref
                out["$q.short"] = n.short
                out["$q.title"] = n.title
                out["$q.state"] = n.state
                out["$q.mark"] = EpicRow.mark(n.state)
                out["$q.note"] = n.note
                out["$q.active"] = n.active
                out["$q.clearable"] = n.clearable
            }
        }

        if (s.hasToolkit()) {
            out["toolkit.state"] = ToolkitList.state(s)
            out["toolkit.waiting"] = ToolkitList.waiting(s)
        }
        ToolkitList.tools(s).forEachIndexed { i, t ->
            val p = "toolkit.tools[$i]"
            out[p] = t.name
            out["$p.pin"] = t.pin
            out["$p.latest"] = t.latest
            out["$p.mark"] = t.mark
            out["$p.tone"] = tone(t.tone)
            out["$p.rolling"] = t.rolling
            out["$p.proposal"] = t.proposal
            out["$p.canRollOut"] = t.canRollOut
            out["$p.repos"] = t.repos
            out["$p.error"] = t.error
        }
        ToolkitList.rollouts(s).forEachIndexed { i, r ->
            val p = "toolkit.rollouts[$i]"
            out[p] = r.id
            out["$p.stage"] = r.stage
            out["$p.tone"] = tone(r.tone)
            out["$p.counts"] = r.counts
            out["$p.summary"] = r.summary
            out["$p.error"] = r.error
            out["$p.pinPR"] = r.pinPr
            out["$p.actions"] = listOfNotNull(
                "cancel".takeIf { r.canCancel }, "retry".takeIf { r.canRetry }, "dismiss".takeIf { r.canDismiss },
            )
            r.targets.forEachIndexed { j, t ->
                val q = "$p.targets[$j]"
                out[q] = t.target
                out["$q.state"] = t.state
                out["$q.tone"] = tone(t.tone)
                out["$q.url"] = t.url
                out["$q.note"] = t.note
            }
        }

        out["summary"] = HubSummary.line(s, now)
        val a = Attention.of(s)
        out["attention.count"] = a.count
        a.sections.forEachIndexed { i, sec ->
            out["attention[$i]"] = sec.id
            out["attention[$i].title"] = sec.title
            sec.rows.forEachIndexed { j, r ->
                val p = "attention[$i][$j]"
                out[p] = r.id
                out["$p.title"] = r.title
                out["$p.detail"] = r.detail
                out["$p.url"] = r.url
                out["$p.updated"] = epoch(r.updated)
                out["$p.approval"] = when (val ap = r.approval) {
                    is Approval.Proposal -> "proposal ${ap.id}"
                    is Approval.Epic -> "epic ${ap.ref} ${ap.planId}"
                    is Approval.Gate -> "gate ${ap.ref} ${ap.piece}"
                    is Approval.Release -> "release ${ap.id} ${ap.tool} ${ap.version}"
                    null -> null
                }
            }
        }
        return out.text()
    }

    private fun item(out: Out, p: String, r: ItemRow, now: Instant) {
        out[p] = r.id
        out["$p.ref"] = r.ref
        out["$p.kind"] = r.kind
        out["$p.marker"] = when (r.marker) {
            Marker.READY -> "ready"
            Marker.YOUR_MOVE -> "yourMove"
            Marker.NEEDS_YOU -> "needsYou"
            null -> null
        }
        out["$p.markerRank"] = r.markerRank
        out["$p.isPR"] = r.isPR
        out["$p.ci"] = r.ci?.let { tone(it) }
        out["$p.review"] = r.review?.let { tone(it) }
        out["$p.statusRank"] = r.statusRank
        out["$p.status"] = r.statusWords
        out["$p.title"] = r.title
        out["$p.author"] = r.author
        out["$p.threads"] = "${r.threads}/${r.botThreads}"
        out["$p.lastActor"] = r.lastActor
        out["$p.updated"] = epoch(r.updated)
        out["$p.age"] = r.updated?.let { ItemList.age(it, now) }
        out["$p.labels"] = r.labels
        out["$p.run"] = r.run?.let { "${it.text(now)} (${tone(it.tone)}, ${it.words})" }
        out["$p.stage"] = r.stage?.let { "${it.text} (${tone(it.tone)})" }
        out["$p.latestRun"] = r.latestRun?.let { "${it.id} ${it.task} live=${it.live} finished=${it.finished} transcript=${it.hasTranscript}" }
    }

    /** Each key's ascending order, ties kept in the list's order. */
    private fun <R> sorts(out: Out, name: String, rows: List<R>, id: (R) -> String, vararg keys: Pair<String, (R) -> Comparable<*>>) {
        for ((k, key) in keys) {
            @Suppress("UNCHECKED_CAST")
            val sorted = rows.withIndex().sortedWith(compareBy({ key(it.value) as Comparable<Any> }, { it.index }))
            out["$name.sort.$k"] = sorted.map { id(it.value) }
        }
    }

    private fun tone(t: Tone) = t.name.lowercase()

    private fun epoch(t: Instant?): Long? = t?.epochSecond

    /** Strings in double quotes, with \, " and newlines escaped. */
    fun quote(s: String) = "\"" + s.replace("\\", "\\\\").replace("\"", "\\\"").replace("\n", "\\n") + "\""

    private class Out {
        private val lines = mutableListOf<String>()

        operator fun set(key: String, value: Any?) {
            val v = when (value) {
                null -> "nil"
                is String -> quote(value)
                is List<*> -> value.joinToString(", ", "[", "]") { quote(it as String) }
                else -> value.toString()
            }
            lines += "$key = $v"
        }

        fun text() = lines.joinToString("\n", postfix = "\n")
    }
}
