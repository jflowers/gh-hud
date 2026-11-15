package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.CIState
import io.github.jflowers.ghhud.api.v1.Item
import io.github.jflowers.ghhud.api.v1.ItemKind
import io.github.jflowers.ghhud.api.v1.Run
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.Stage
import java.time.Instant

// The dashboard's item list, worked out from a hub snapshot: the filters
// (the TUI's tabs 0-5, 7 and 8), search, and what each row shows. A port
// of the Mac's ItemList.swift; testdata/clients keeps the two in step.

/** How a status reads, for the UI to color. */
public enum class Tone {
    GOOD, BAD, WARN, PENDING, MUTED;

    /**
     * Most in need of attention first: failed, then warnings, then in
     * progress, then fine, then nothing to say.
     */
    public val rank: Int get() = when (this) {
        BAD -> 0
        WARN -> 1
        PENDING -> 2
        GOOD -> 3
        MUTED -> 4
    }
}

/** Seconds from a to b, as the Mac's timeIntervalSince. */
internal fun secondsBetween(a: Instant, b: Instant): Double =
    (b.epochSecond - a.epochSecond) + (b.nano - a.nano) / 1e9

/** A filter over the items: one of the dashboard's sections, or all. */
public enum class ItemFilter(public val id: String, public val title: String, public val category: Category?) {
    ALL("all", "All items", null),
    REVIEW("review", "Review requested", Category.REVIEW_REQUESTED),
    MINE("mine", "Mine", Category.MINE),
    ISSUES("issues", "Issues", Category.ISSUES),
    CI("ci", "CI", Category.CI),
    INBOX("inbox", "Inbox", Category.NOTIFICATIONS),
    READY("ready", "Ready for a maintainer", Category.READY_FOR_MAINTAINER),
    QUEUE("queue", "Agent review queue", Category.READY_FOR_AGENT_REVIEW),
    ;

    /** A short name, for chips. */
    public val shortTitle: String get() = when (this) {
        ALL -> "All"
        REVIEW -> "Review"
        READY -> "Ready"
        QUEUE -> "Agent queue"
        else -> title
    }

    public fun matches(it: Item): Boolean = category?.let { c -> it.isIn(c) } ?: true
}

/** Why a row stands out, the TUI's ◆. */
public enum class Marker {
    /** Your PR, waiting only on a maintainer (green). */
    READY,

    /** The next move is yours (yellow). */
    YOUR_MOVE,

    /** An agent's next step waits on you (orange). */
    NEEDS_YOU,
}

/** One row of the item list. */
public data class ItemRow(
    val id: String, // the URL
    val kind: String, // "mine", "review", "issue", "ci", "inbox"
    val marker: Marker?,
    val isPR: Boolean,
    val ci: Tone?,
    val review: Tone?,
    val conflict: Boolean,
    val num: Int,
    val repo: String,
    val title: String,
    val draft: Boolean,
    val author: String,
    /** Unresolved threads opened by people, and by bots. */
    val threads: Int,
    val botThreads: Int,
    /** Who acted last: "you" when it was you. */
    val lastActor: String,
    val updated: Instant?,
    val labels: List<String>,
    /** The item's newest agent run, while it's worth showing. */
    val run: RunTag?,
    /** What the agents do with it next, if anything. */
    val stage: StageTag?,
    /**
     * The newest run on the item, shown or not: Watch, Re-check and the
     * transcript act on it.
     */
    val latestRun: RunRef?,
) : ColumnFilterable {
    /** "owner/repo#N", or the URL when the item has no number. */
    val ref: String get() = if (num > 0) "$repo#$num" else id

    override val filterColumns: List<String> get() = COLUMNS

    override fun filterValues(column: String): List<String> = when (column) {
        "kind" -> listOf(kind)
        "status" -> statusWords
        "repo" -> listOf(repo)
        "title" -> listOf(title)
        "author" -> if (author.isEmpty()) emptyList() else listOf(author)
        "last" -> if (lastActor.isEmpty()) emptyList() else listOf(lastActor)
        "label" -> labels
        else -> emptyList()
    }

    /** The status cluster in words: "ci failing", "approved", "conflict". */
    val statusWords: List<String> get() = buildList {
        when (ci) {
            Tone.GOOD -> add("ci passing")
            Tone.BAD -> add("ci failing")
            Tone.PENDING -> add("ci pending")
            Tone.MUTED -> add("no checks")
            else -> {}
        }
        when (review) {
            Tone.GOOD -> add("approved")
            Tone.BAD -> add("changes requested")
            Tone.WARN -> add("review required")
            Tone.PENDING -> add("review pending")
            else -> {}
        }
        if (conflict) add("conflict")
        if (draft) add("draft")
    }

    // Sort keys, as the Mac's table headers sort.

    /** Needs you, your move, ready, then the rest. */
    val markerRank: Int get() = when (marker) {
        Marker.NEEDS_YOU -> 0
        Marker.YOUR_MOVE -> 1
        Marker.READY -> 2
        null -> 3
    }

    /** The status cluster: a conflict or the worst of CI and review. */
    val statusRank: Int get() =
        if (conflict) Tone.BAD.rank else listOfNotNull(ci?.rank, review?.rank).minOrNull() ?: (Tone.MUTED.rank + 1)

    /** Issues without a number sort last. */
    val numKey: Long get() = if (num > 0) num.toLong() else Long.MAX_VALUE

    /** People's threads first, bots' breaking ties. */
    val threadsKey: Int get() = threads * 1000 + botThreads

    val ageKey: Double get() = SortKey.age(updated)

    public companion object {
        public val COLUMNS: List<String> = listOf("kind", "status", "repo", "title", "author", "last", "label")

        public fun of(it: Item, me: String, run: Run? = null, stage: Stage? = null, now: Instant = Instant.now()): ItemRow =
            ItemRow(
                id = it.url,
                kind = kind(it),
                marker = when {
                    stage?.kind == "you" -> Marker.NEEDS_YOU
                    it.isIn(Category.READY_FOR_MAINTAINER) -> Marker.READY
                    it.waitingOn == "me" -> Marker.YOUR_MOVE
                    else -> null
                },
                isPR = it.kind == ItemKind.ITEM_KIND_PR || it.kind == ItemKind.ITEM_KIND_RUN,
                ci = tone(it.ci),
                review = reviewTone(it.review),
                conflict = it.merge == "⚠️",
                num = it.num.toInt(),
                repo = it.repo,
                title = it.title,
                draft = it.draft,
                author = it.author,
                threads = it.threads.toInt(),
                botThreads = it.botThreads.toInt(),
                lastActor = if (me.isNotEmpty() && it.lastActor == me) "you" else it.lastActor,
                updated = if (it.hasUpdatedAt()) it.updatedAt.instant else null,
                labels = it.labelsList,
                run = run?.let { RunTag.of(it, now) },
                stage = stage?.let { StageTag.of(it) },
                latestRun = run?.let { RunRef.of(it) },
            )

        /** The kind badge, as the TUI picks it. */
        internal fun kind(it: Item): String = when {
            it.kind == ItemKind.ITEM_KIND_RUN -> "ci"
            it.isIn(Category.MINE) -> "mine"
            it.isIn(Category.REVIEW_REQUESTED) -> "review"
            it.isIn(Category.ISSUES) -> "issue"
            else -> "inbox"
        }

        internal fun tone(s: CIState): Tone? = when (s) {
            CIState.CI_STATE_OK -> Tone.GOOD
            CIState.CI_STATE_FAIL -> Tone.BAD
            CIState.CI_STATE_PENDING -> Tone.PENDING
            CIState.CI_STATE_NONE -> Tone.MUTED
            else -> null
        }

        /** The hub sends the review state as the TUI's emoji. */
        internal fun reviewTone(r: String): Tone? = when (r) {
            "✅" -> Tone.GOOD
            "❌", "‼️" -> Tone.BAD
            "❗" -> Tone.WARN
            "🟡" -> Tone.PENDING
            else -> null
        }
    }
}

/** An agent run on a row: "fix ● 5m", the TUI's run tag. */
public data class RunTag(val task: String, val status: String, val tone: Tone, val at: Instant) {
    /** The status in words, as the TUI says it. */
    val words: String get() = words(status)

    /** "fix ● 5m". */
    public fun text(now: Instant = Instant.now()): String = "$task ${glyph(status)} ${ItemList.age(at, now)}"

    public companion object {
        /**
         * Shown while the run is live, for a day after it succeeded, and
         * for a week after it failed or left work behind (until a newer run).
         */
        public fun of(r: Run, now: Instant = Instant.now()): RunTag? {
            val end = (if (r.hasFinishedAt()) r.finishedAt else r.startedAt).instant
            val age = secondsBetween(end, now)
            when (r.status) {
                "running", "blocked" -> {}
                "failed", "left-work" -> if (age >= 7 * 86400) return null
                else -> if (age >= 86400) return null
            }
            return RunTag(r.task, r.status, tone(r.status), end)
        }

        public fun words(status: String): String = when (status) {
            "running" -> "running"
            "blocked" -> "blocked, needs input"
            "ok" -> "succeeded"
            "failed" -> "failed"
            "left-work" -> "left work behind"
            else -> "outcome unknown"
        }

        /** How a run status reads. */
        public fun tone(status: String): Tone = when (status) {
            "running" -> Tone.PENDING
            "blocked", "left-work" -> Tone.WARN
            "ok" -> Tone.GOOD
            "failed" -> Tone.BAD
            else -> Tone.MUTED
        }

        internal fun glyph(status: String): String = when (status) {
            "running" -> "●"
            "blocked" -> "◆"
            "ok" -> "✓"
            "failed" -> "✗"
            "left-work" -> "!"
            else -> "?"
        }
    }
}

/** What the agents do with an item next: "▸review", "▸merge", "▸you: …". */
public data class StageTag(val text: String, val tone: Tone) {
    public companion object {
        public fun of(st: Stage): StageTag? = when (st.kind) {
            "next" -> StageTag("▸" + st.task, Tone.MUTED)
            "wait" -> {
                val why = if (st.reason.isEmpty() || st.reason == "cooldown") "wait" else st.reason
                StageTag("▸${st.task} $why", Tone.MUTED)
            }
            "you" -> StageTag("▸you: ${st.task} ${st.reason}".trim(' ', '\t'), Tone.WARN)
            "merge" -> StageTag(if (st.reason.isEmpty()) "▸merge" else "▸merge ${st.reason}", Tone.GOOD)
            else -> null
        }
    }
}

/** The run the run commands act on. */
public data class RunRef(
    val id: String,
    val task: String,
    /** Running or blocked: there is a pane to watch. */
    val live: Boolean,
    /** Finished: its outcome can be inspected again. */
    val finished: Boolean,
    /** It has an opencode session to open. */
    val hasTranscript: Boolean,
) {
    public companion object {
        public fun of(r: Run): RunRef = RunRef(
            id = r.id,
            task = r.task,
            live = r.status == "running" || r.status == "blocked",
            finished = r.hasFinishedAt(),
            hasTranscript = r.sessionId.isNotEmpty(),
        )
    }
}

public object ItemList {
    /**
     * The rows a filter and a search show, in the hub's order (most
     * pressing first). The search's text matches repo, title and ref,
     * ignoring case, like the TUI's /; its column terms (`kind:issue`)
     * and the kept filters narrow it further.
     */
    public fun rows(
        s: Snapshot,
        filter: ItemFilter,
        query: String = "",
        kept: List<ColumnFilter> = emptyList(),
        now: Instant = Instant.now(),
    ): List<ItemRow> {
        val parsed = TableQuery.parse(query, ItemRow.COLUMNS)
        val q = parsed.text.trim(' ', '\t').lowercase()
        val filters = kept + parsed.filters
        val runs = latestRuns(s)
        return s.result.itemsList
            .filter { filter.matches(it) }
            .filter { q.isEmpty() || "${it.repo} ${it.title} ${it.ref}".lowercase().contains(q) }
            .map { ItemRow.of(it, s.result.user, runs[it.ref], s.stagesMap[it.url], now) }
            .filter { it.passes(filters) }
    }

    /** Each ref's newest run (the hub sends runs newest first). */
    public fun latestRuns(s: Snapshot): Map<String, Run> {
        val out = LinkedHashMap<String, Run>()
        for (r in s.runsList) out.putIfAbsent(r.ref, r)
        return out
    }

    /**
     * Why commands are off, or null when they work: the hub has no
     * dispatcher, or another gh-hud owns dispatch.
     */
    public fun commandsOff(s: Snapshot): String? = when {
        !s.dispatch -> "Dispatch is off on the hub"
        s.follower.isNotEmpty() -> "Read-only: gh-hud pid ${s.follower} owns dispatch"
        else -> null
    }

    /** How many items each filter shows (before searching). */
    public fun counts(s: Snapshot): Map<ItemFilter, Int> =
        ItemFilter.entries.associateWith { f -> s.result.itemsList.count { f.matches(it) } }

    /** "now", "5m", "3h", "2d", "1w", "4mo": the TUI's age column. */
    public fun age(d: Instant, now: Instant = Instant.now()): String {
        val mins = maxOf(0, (secondsBetween(d, now) / 60).toInt())
        return when {
            mins < 1 -> "now"
            mins < 60 -> "${mins}m"
            mins < 1440 -> "${mins / 60}h"
            mins < 10080 -> "${mins / 1440}d"
            mins < 43200 -> "${mins / 10080}w"
            else -> "${mins / 43200}mo"
        }
    }
}

internal object SortKey {
    /** An age column: ascending is newest first, no date last. */
    fun age(d: Instant?): Double = d?.let { -(it.epochSecond + it.nano / 1e9) } ?: Double.POSITIVE_INFINITY

    /** A duration column: unknown last. */
    fun duration(t: Double?): Double = t ?: Double.POSITIVE_INFINITY
}

/**
 * The orders the item list offers: the Mac's column sorts. HUB keeps the
 * hub's (most pressing first). Ties keep the hub's order either way.
 */
public enum class ItemSort(public val label: String) {
    HUB("Hub's order"),
    NEEDS_YOU("Needs you"),
    STATUS("Status"),
    NUMBER("Number"),
    THREADS("Threads"),
    AGE("Newest"),
    ;

    /** The rows in this order, or reversed; HUB is never reversed. */
    public fun sort(rows: List<ItemRow>, reversed: Boolean = false): List<ItemRow> {
        val c: Comparator<ItemRow> = when (this) {
            HUB -> return rows
            NEEDS_YOU -> compareBy { it.markerRank }
            STATUS -> compareBy { it.statusRank }
            NUMBER -> compareBy { it.numKey }
            THREADS -> compareByDescending { it.threadsKey } // most first, as the Mac's descending
            AGE -> compareBy { it.ageKey }
        }
        return rows.sortedWith(if (reversed) c.reversed() else c)
    }
}
