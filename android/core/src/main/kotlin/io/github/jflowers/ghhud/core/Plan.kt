package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.Case
import io.github.jflowers.ghhud.api.v1.Decision
import io.github.jflowers.ghhud.api.v1.Epic
import io.github.jflowers.ghhud.api.v1.Item
import io.github.jflowers.ghhud.api.v1.PlanRow
import io.github.jflowers.ghhud.api.v1.Snapshot

// The Plan, worked out from a hub snapshot: the TUI's tab p. What agents
// work on next in start order (your queue, then the auto steps due), what's
// running, waiting, stopped and merging; above them the open cases and the
// enrolled epics. A port of the Mac's Plan.swift.

/** Words on one line: runs of whitespace become one space. */
internal fun oneLine(s: String): String = s.split(Regex("\\s+")).filter { it.isNotEmpty() }.joinToString(" ")

private val scheme = Regex("^[A-Za-z][A-Za-z0-9+.-]*:")

/** One row of the plan. */
public data class PlanItem(
    val id: String, // task and ref, unique in a plan
    val group: String, // "Your queue", "Auto, next", "Running"…
    /** Start order (1 first) for rows the next pass tries to start. */
    val position: Int?,
    val task: String,
    val ref: String,
    val title: String,
    val url: String?,
    val status: String,
    val tone: Tone,
    /** Waiting, merging or missing: drawn dimmed. */
    val quiet: Boolean,
    /** Your queue's place (1 first); null for an auto row. */
    val queued: Int?,
    /** Queued, but not in the latest refresh. */
    val missing: Boolean,
    /** The epic a piece comes from. */
    val epic: String,
    /** A piece held by a gate only you can clear. */
    val gated: Boolean,
    val item: Item,
) {
    public companion object {
        internal fun of(r: PlanRow, position: Int?): PlanItem {
            val it = r.item
            val ref = if (it.num > 0) "${it.repo}#${it.num}" else it.url
            val (status, tone) = status(r)
            return PlanItem(
                id = r.task + "|" + ref,
                group = group(r),
                position = position,
                task = r.task,
                ref = ref,
                title = it.title,
                url = it.url.takeIf { u -> scheme.containsMatchIn(u) } ?: Links.forRef(ref),
                status = status,
                tone = tone,
                quiet = r.stage.kind == "wait" || r.stage.kind == "merge" || r.missing,
                queued = if (r.queued > 0) r.queued.toInt() else null,
                missing = r.missing,
                epic = r.epic,
                gated = r.epic.isNotEmpty() && r.stage.reason.startsWith("gate: "),
                item = it,
            )
        }

        /** Whether the next pass tries to start it. */
        internal fun starts(r: PlanRow): Boolean =
            if (r.queued > 0) !r.missing && r.stage.kind != "running" else r.stage.kind == "next"

        internal fun group(r: PlanRow): String {
            if (r.queued > 0) return "Your queue"
            return when (r.stage.kind) {
                "next" -> "Auto, next"
                "running" -> "Running"
                "wait" -> "Waiting"
                "you" -> "Needs you"
                "merge" -> "Merging"
                else -> "Other"
            }
        }

        internal fun status(r: PlanRow): Pair<String, Tone> {
            val kind = r.stage.kind
            if (r.queued > 0) {
                if (r.missing || kind == "running") return r.note to Tone.MUTED
                if (r.note == "next free slot" || r.note == "starting") return r.note to Tone.GOOD
                return r.note to Tone.BAD
            }
            if (r.epic.isNotEmpty() && kind == "next") return "next free slot · epic ${r.epic}" to Tone.GOOD
            if (r.epic.isNotEmpty() && kind == "running") return "running · epic ${r.epic}" to Tone.PENDING
            return when (kind) {
                "next" -> "next free slot" to Tone.GOOD
                "running" -> "running" to Tone.PENDING
                "wait" -> r.stage.reason to Tone.MUTED
                "you" -> r.stage.reason to Tone.WARN
                "merge" -> r.stage.reason.ifEmpty { "auto-merge on" } to Tone.GOOD
                else -> "" to Tone.MUTED
            }
        }
    }
}

/** A group of plan rows under a heading. */
public data class PlanGroup(val title: String, val rows: List<PlanItem>)

/** A check failing on several of your PRs, and what the supervisor does about it. */
public data class CaseRow(
    val id: String,
    val title: String, // "acme/r lint", "acme/r#12 review threads"
    val scope: String, // "2 PRs, main too"; "" for review threads
    val status: String,
    val tone: Tone,
    /** The proposal's summary, on one line. */
    val summary: String,
    /** Set when a proposal waits for you. */
    val proposal: String?,
    /** The drafted replies a review_reply proposal posts, as you. */
    val replies: List<Reply> = emptyList(),
) {
    /** A drafted reply to a review thread, under the comment it answers. */
    public data class Reply(val place: String, val author: String, val quote: String, val body: String)

    val isReview: Boolean get() = id.startsWith("review_reply|")

    public companion object {
        public fun of(c: Case): CaseRow {
            val prs = c.facts.prsCount
            val review = c.kind == "review_reply"
            val waiting = c.hasProposal() && c.proposal.id.isNotEmpty() &&
                (c.proposal.status.isEmpty() || c.proposal.status == "pending")
            val (status, tone) = when {
                waiting -> (if (review) "supervisor drafted " else "supervisor proposes ") + reason(c.proposal.decision) to Tone.WARN
                c.hasDecision() ->
                    reason(c.decision) + (if (c.decision.by.isEmpty()) "" else " (${c.decision.by})") to Tone.MUTED
                c.supervising -> (if (review) "supervisor drafting replies…" else "supervisor looking…") to Tone.PENDING
                else -> "needs you" to Tone.WARN
            }
            return CaseRow(
                id = c.id,
                title = if (review) "${c.repo}#${c.pr} review threads" else "${c.repo} ${c.check}",
                scope = if (review) "" else "$prs PR${if (prs == 1) "" else "s"}" + if (c.facts.onBase) ", main too" else "",
                status = status,
                tone = tone,
                summary = if (waiting) oneLine(c.proposal.decision.summary) else "",
                proposal = if (waiting) c.proposal.id else null,
                replies = if (waiting) {
                    c.proposal.decision.repliesList.map {
                        Reply(if (it.line > 0) "${it.path}:${it.line}" else it.path, it.author, oneLine(it.quote), it.body)
                    }
                } else {
                    emptyList()
                },
            )
        }

        internal fun reason(d: Decision): String = when (d.kind) {
            "answer" -> if (d.repliesCount == 1) "1 reply" else "${d.repliesCount} replies"
            "link_fix" -> "fix in #${d.fixPr}"
            else -> "escalated: " + oneLine(d.summary)
        }
    }
}

/** An enrolled epic: where its plan stands, and its pieces still to do. */
public data class EpicRow(
    val ref: String,
    val title: String,
    val url: String?,
    val status: String,
    val tone: Tone,
    /** The plan to approve, when one waits for you. */
    val planToApprove: String?,
    /**
     * What that plan changed since you last approved one, a line each:
     * set only when it waits for you again.
     */
    val changes: List<String> = emptyList(),
    /**
     * The plan whose pieces are all finished, when the epic can be finished
     * (gh-hud#22), and the comment finishing it posts as you.
     */
    val planToFinish: String? = null,
    val summary: String = "",
    /** Set when the run budget is spent: approving the plan again grants this many more runs. */
    val budgetToGrant: Int? = null,
    /** Changes the supervisor proposes to the plan, waiting for you; approving makes them on GitHub, as you. */
    val replans: List<Replan> = emptyList(),
    /** Pieces still to do, running and ready first. */
    val open: List<Piece>,
    val done: Int,
    val total: Int,
) {
    val id: String get() = ref

    /** A proposed change to the plan: "drop #12 (close it as not planned)", and why. */
    public data class Replan(val id: String, val what: String, val why: String)

    public data class Piece(
        val ref: String, // owner/repo#N
        val short: String, // "#N", or shorter for the epic's owner
        val title: String,
        val state: String,
        val note: String,
        /** Ready, in progress or running: drawn bright. */
        val active: Boolean,
        /** Held by a gate only you can clear (on an approved epic). */
        val clearable: Boolean,
    )

    public companion object {
        private val RANK = mapOf(
            "running" to 0, "ready" to 1, "in_progress" to 2, "gated" to 3, "blocked" to 4, "waiting" to 5, "unknown" to 6,
        )

        public fun of(e: Epic): EpicRow {
            val nodes = if (e.hasPlan()) e.plan.nodesList else emptyList()
            val done = nodes.count { it.state == "done" }
            fun count(s: String) = nodes.count { it.state == s }
            var counts = "$done/${nodes.size} done, "
            if (count("running") > 0) counts += "${count("running")} running, "
            counts += "${count("ready")} ready, ${count("in_progress")} in progress"
            val (status, tone) = when {
                e.state == "reading" -> "reading the plan…" to Tone.PENDING
                e.state == "error" -> "can't read it: ${e.error}" to Tone.BAD
                e.state == "closed" -> (e.closedNote + if (e.hasPlan()) " · $counts" else "") to Tone.MUTED
                !e.hasPlan() -> "no plan yet" to Tone.MUTED
                e.plan.stale -> "the epic changed: reread it · $counts" to Tone.WARN
                e.state == "planned" -> "plan waits for you · $counts" to Tone.WARN
                e.hasComplete() -> "every piece finished · $counts" to Tone.WARN
                e.held.isNotEmpty() -> "held by the ${e.held} label · $counts" to Tone.WARN
                budgetSpent(e) -> "run budget spent (${e.runs}/${e.runBudget} runs) · $counts" to Tone.WARN
                else -> "approved · $counts" + (if (e.runBudget > 0) " · ${e.runs}/${e.runBudget} runs" else "") to Tone.GOOD
            }
            val repo = e.ref.split("#", limit = 2).first()
            val approved = e.state == "approved"
            val open = nodes.withIndex()
                .filter { it.value.state != "done" && it.value.state != "dropped" }
                .sortedWith(compareBy({ RANK[it.value.state] ?: 7 }, { it.index }))
                .map { (_, n) ->
                    Piece(
                        ref = n.ref,
                        short = short(repo, n.ref),
                        title = n.title,
                        state = n.state,
                        note = listOf(n.note, if (n.areaCount > 0) n.areaList.joinToString(", ", "[", "]") else "")
                            .filter { it.isNotEmpty() }.joinToString("  "),
                        active = n.state in setOf("ready", "in_progress", "running"),
                        clearable = approved && n.state == "gated" && n.gate.isNotEmpty() && !n.cleared,
                    )
                }
            val spent = budgetSpent(e) && e.hasPlan() && !e.plan.stale && e.plan.id.isNotEmpty()
            val toApprove = if ((e.state == "planned" || spent) && e.hasPlan() && !e.plan.stale && e.plan.id.isNotEmpty()) e.plan.id else null
            val toFinish = if (e.state == "approved" && e.hasComplete() && e.hasPlan() && !e.plan.stale && e.summary.isNotEmpty()) e.plan.id else null
            return EpicRow(
                ref = e.ref,
                title = e.title,
                url = e.url.ifEmpty { null } ?: Links.forRef(e.ref),
                status = status,
                tone = tone,
                planToApprove = toApprove,
                changes = if (toApprove == null) emptyList() else e.changesList,
                planToFinish = toFinish,
                summary = if (toFinish == null) "" else e.summary,
                budgetToGrant = if (spent) e.runBudget.toInt() else null,
                replans = e.replansList.filter { it.status == "pending" }.map {
                    val what = if (it.kind == "drop") {
                        "drop ${short(repo, it.piece)} (close it as not planned)"
                    } else {
                        "new piece \u201C${it.title}\u201D, from ${short(repo, it.piece)}"
                    }
                    Replan(it.id, what, it.why)
                },
                open = open,
                done = done,
                total = nodes.size,
            )
        }

        /** The epic's pieces used its run budget: nothing starts until you approve the plan again. */
        internal fun budgetSpent(e: Epic): Boolean = e.state == "approved" && e.runBudget > 0 && e.runs >= e.runBudget

        /** A piece's ref, shortened within the epic's repo and owner. */
        internal fun short(epicRepo: String, ref: String): String {
            if (ref.startsWith("$epicRepo#")) return ref.drop(epicRepo.length)
            val owner = epicRepo.split("/").first()
            if (ref.startsWith("$owner/")) return ref.drop(owner.length + 1)
            return ref
        }

        /** The TUI's piece marks. */
        public fun mark(state: String): String = when (state) {
            "done" -> "✓"
            "dropped" -> "✗"
            "in_progress" -> "▸"
            "running" -> "▶"
            "ready" -> "●"
            "waiting" -> "◌"
            "gated" -> "⏸"
            "blocked" -> "!"
            else -> "?"
        }
    }
}

public object PlanList {
    /**
     * The plan rows under their headings, in start order; the rows the
     * next pass tries to start are numbered.
     */
    public fun groups(s: Snapshot): List<PlanGroup> {
        val groups = mutableListOf<PlanGroup>()
        var pos = 0
        var rows = mutableListOf<PlanItem>()
        var title = ""
        for (r in s.planList) {
            val g = PlanItem.group(r)
            if (g != title && rows.isNotEmpty()) {
                groups += PlanGroup(title, rows)
                rows = mutableListOf()
            }
            title = g
            var at: Int? = null
            if (r.queued > 0 || r.stage.kind == "next") {
                pos += 1
                at = pos
            }
            rows += PlanItem.of(r, at)
        }
        if (rows.isNotEmpty()) groups += PlanGroup(title, rows)
        return groups
    }

    /** Rows the next pass tries to start: the count on the tab. */
    public fun starting(s: Snapshot): Int = s.planList.count { PlanItem.starts(it) }

    /** "4 start next · slots chai 1/2, local 0/1 · read-only". */
    public fun summary(s: Snapshot): String {
        val parts = mutableListOf("${starting(s)} start next")
        if (s.usageCount > 0) {
            parts += "slots " + s.usageList.joinToString(", ") { if (it.ok) "${it.name} ${it.used}/${it.max}" else "${it.name} —" }
        }
        if (s.follower.isNotEmpty()) parts += "read-only"
        return parts.joinToString(" · ")
    }

    public fun cases(s: Snapshot): List<CaseRow> = s.casesList.map { CaseRow.of(it) }

    public fun epics(s: Snapshot): List<EpicRow> = s.epicsList.map { EpicRow.of(it) }
}
