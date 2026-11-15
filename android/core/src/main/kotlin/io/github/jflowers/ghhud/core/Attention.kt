package io.github.jflowers.ghhud.core

import com.google.protobuf.Timestamp
import io.github.jflowers.ghhud.api.v1.Item
import io.github.jflowers.ghhud.api.v1.Snapshot
import java.time.Duration
import java.time.Instant

// What needs you, worked out from a hub snapshot: the phone's counterpart
// of the Mac menu (macos/Sources/GHHudKit/Attention.swift). Pure
// functions, so the JVM tests cover them without a hub or a UI.

/** Dashboard section bits (Item.categories), as the hub defines them. */
public enum class Category(public val bit: Int) {
    REVIEW_REQUESTED(1),
    MINE(2),
    ISSUES(4),
    CI(8),
    NOTIFICATIONS(16),
    READY_FOR_MAINTAINER(32),
    READY_FOR_AGENT_REVIEW(64),
}

public fun Item.isIn(c: Category): Boolean = categories and c.bit != 0

/** "owner/repo#N", or the URL when the item has no number. */
public val Item.ref: String get() = if (num > 0) "$repo#$num" else url

internal val Timestamp.instant: Instant get() = Instant.ofEpochSecond(seconds, nanos.toLong())

/** Something you can approve from the phone. */
public sealed interface Approval {
    /** A supervisor proposal (Case.proposal), approved or rejected by id. */
    public data class Proposal(val id: String) : Approval

    /** An epic's plan, approved by the epic's ref and the plan seen. */
    public data class Epic(val ref: String, val planId: String) : Approval

    /** A gate on a piece of an approved epic, cleared by both refs. */
    public data class Gate(val ref: String, val piece: String) : Approval

    /** Whether it can be rejected: only a proposal. */
    public val rejectable: Boolean get() = this is Proposal

    /** The button that says yes. */
    public val verb: String get() = if (this is Gate) "Clear" else "Approve"
}

/** One row on the screen. */
public data class AttentionRow(
    val id: String,
    val title: String,
    val detail: String,
    val url: String?,
    val updated: Instant?,
    /** Set when the row waits for your approval. */
    val approval: Approval? = null,
)

/** A titled group of rows. */
public data class AttentionSection(val id: String, val title: String, val rows: List<AttentionRow>)

public data class Attention(
    /** Sections that have rows, in screen order. */
    val sections: List<AttentionSection>,
    /** How many things need you. */
    val count: Int,
) {
    public companion object {
        public val EMPTY: Attention = Attention(emptyList(), 0)

        /**
         * What needs you in a snapshot: agents waiting on you, supervisor
         * proposals and epic plans to approve, epic gates to clear,
         * reviews requested of you, and your PRs ready for a maintainer.
         */
        public fun of(s: Snapshot): Attention {
            val items = s.result.itemsList
            fun rows(c: Category) = items.filter { it.isIn(c) }
                .sortedByDescending { it.updatedAt.instant }
                .map {
                    AttentionRow(
                        id = it.url,
                        title = it.title,
                        detail = if (it.author.isEmpty()) it.ref else "${it.ref} · ${it.author}",
                        url = it.url.ifEmpty { null },
                        updated = if (it.hasUpdatedAt()) it.updatedAt.instant else null,
                    )
                }
            val blocked = s.sessionsList.filter { it.blocked }
                .sortedBy { it.blockedSince.instant } // longest waiting first
                .map { se ->
                    AttentionRow(
                        id = "session:" + se.id,
                        title = se.title.ifEmpty { se.id },
                        detail = listOf(se.task, se.ref, se.machine.ifEmpty { "local" })
                            .filter { it.isNotEmpty() }.joinToString(" · "),
                        url = Links.forRef(se.ref),
                        updated = if (se.hasBlockedSince()) se.blockedSince.instant else null,
                    )
                }
            val all = listOf(
                AttentionSection("blocked", "Agents waiting on you", blocked),
                AttentionSection("proposals", "Proposals awaiting approval", proposals(s)),
                AttentionSection("epics", "Epic plans awaiting approval", epics(s)),
                AttentionSection("gates", "Epic gates only you can clear", gates(s)),
                AttentionSection("review", "Review requested", rows(Category.REVIEW_REQUESTED)),
                AttentionSection("ready", "Ready for a maintainer", rows(Category.READY_FOR_MAINTAINER)),
            )
            return Attention(all.filter { it.rows.isNotEmpty() }, all.sumOf { it.rows.size })
        }

        /** Cases whose supervisor proposal waits for you, oldest first. */
        internal fun proposals(s: Snapshot): List<AttentionRow> =
            s.casesList.filter { c ->
                c.hasProposal() && c.proposal.id.isNotEmpty() &&
                    (c.proposal.status.isEmpty() || c.proposal.status == "pending")
            }
                .sortedBy { it.proposal.created.instant }
                .map { c ->
                    val p = c.proposal
                    val d = p.decision
                    val summary = d.summary.lineSequence().firstOrNull().orEmpty()
                    val kind = d.kind.replace('_', ' ')
                    val title = summary.ifEmpty { "${kind.ifEmpty { "proposal" }} for ${c.repo}" }
                    val pr = if (d.fixPr > 0) d.fixPr else c.facts.prsList.firstOrNull() ?: 0
                    AttentionRow(
                        id = "proposal:" + p.id,
                        title = title,
                        detail = listOf(kind, c.repo, c.check).filter { it.isNotEmpty() }.joinToString(" · "),
                        url = if (pr > 0) Links.forRef("${c.repo}#$pr") else null,
                        updated = if (p.hasCreated()) p.created.instant else null,
                        approval = Approval.Proposal(p.id),
                    )
                }

        /**
         * Epics whose current plan waits for approval. A stale plan (the
         * epic changed since it was read) isn't offered: the hub reads it
         * again.
         */
        internal fun epics(s: Snapshot): List<AttentionRow> =
            s.epicsList.filter { it.state == "planned" && it.hasPlan() && !it.plan.stale && it.plan.id.isNotEmpty() }
                .map { e ->
                    val open = e.plan.nodesList.count { it.state != "done" && it.state != "dropped" }
                    AttentionRow(
                        id = "epic:" + e.ref,
                        title = e.title.ifEmpty { e.ref },
                        detail = "${e.ref} · ${e.plan.nodesCount} pieces, $open open",
                        url = e.url.ifEmpty { null } ?: Links.forRef(e.ref),
                        updated = if (e.plan.hasCreated()) e.plan.created.instant else null,
                        approval = Approval.Epic(e.ref, e.plan.id),
                    )
                }

        /**
         * Pieces of approved epics held only by a gate: everything they
         * wait on is done, and the condition is yours to say holds.
         */
        internal fun gates(s: Snapshot): List<AttentionRow> =
            s.epicsList.filter { it.state == "approved" && it.hasPlan() }
                .flatMap { e ->
                    e.plan.nodesList.filter { it.state == "gated" && it.gate.isNotEmpty() && !it.cleared }
                        .map { n ->
                            AttentionRow(
                                id = "gate:${e.ref} ${n.ref}",
                                title = n.gate,
                                detail = listOf(n.ref, n.title, "epic " + e.ref)
                                    .filter { it.isNotEmpty() }.joinToString(" · "),
                                url = Links.forRef(n.ref),
                                updated = null,
                                approval = Approval.Gate(e.ref, n.ref),
                            )
                        }
                }
    }
}

/** The hub's state in a few words, for the top of the screen. */
public object HubSummary {
    public fun line(s: Snapshot, now: Instant = Instant.now()): String {
        if (s.fetchError.isNotEmpty()) return "GitHub: ${s.fetchError}"
        val parts = mutableListOf<String>()
        if (s.hasResult() && s.result.user.isNotEmpty()) parts += s.result.user
        parts += when {
            s.fetching -> "refreshing…"
            s.hasFetchedAt() -> "updated ${ago(s.fetchedAt.instant, now)}"
            else -> "first refresh…"
        }
        if (s.follower.isNotEmpty()) parts += "read-only: gh-hud pid ${s.follower} owns dispatch"
        if (s.hasFleet()) {
            if (s.fleet.failed.isNotEmpty()) {
                parts += "fleet failed on ${s.fleet.failed}"
            } else if (s.fleet.paused) {
                parts += "fleet paused"
            }
        }
        return parts.joinToString(" · ")
    }

    /** "now", "3m ago", "2h ago", "4d ago". */
    public fun ago(t: Instant, now: Instant): String {
        val secs = Duration.between(t, now).seconds.coerceAtLeast(0)
        return when {
            secs < 60 -> "now"
            secs < 3600 -> "${secs / 60}m ago"
            secs < 86400 -> "${secs / 3600}h ago"
            else -> "${secs / 86400}d ago"
        }
    }
}

public object Links {
    /**
     * A GitHub link for "owner/repo#N". /issues/N redirects to the PR when
     * N is one, so this works for both.
     */
    public fun forRef(ref: String): String? {
        val parts = ref.split("#", limit = 2)
        if (parts.size != 2 || !parts[0].contains("/")) return null
        val n = parts[1].toIntOrNull() ?: return null
        return if (n > 0) "https://github.com/${parts[0]}/issues/$n" else null
    }
}
