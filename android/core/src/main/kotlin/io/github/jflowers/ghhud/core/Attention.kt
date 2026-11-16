package io.github.jflowers.ghhud.core

import com.google.protobuf.Timestamp
import io.github.jflowers.ghhud.api.v1.Item
import io.github.jflowers.ghhud.api.v1.Snapshot
import io.github.jflowers.ghhud.api.v1.ToolkitRolloutState
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

    /**
     * A tool release proposed for rollout (a toolkit rollout in stage
     * "proposed"): approving rolls it out, declining keeps it from being
     * proposed again.
     */
    public data class Release(val id: String, val tool: String, val version: String) : Approval

    /** Whether it can be turned down: a proposal or a release. */
    public val rejectable: Boolean get() = this is Proposal || this is Release

    /** The button that says yes. */
    public val verb: String get() = if (this is Gate) "Clear" else "Approve"

    /** The button that says no, when there is one. */
    public val rejectVerb: String get() = if (this is Release) "Decline" else "Reject"
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
         * proposals and epic plans to approve, epic gates to clear, tool
         * releases to roll out, repos in tool rollouts held, refused or
         * failing, reviews requested of you, and your PRs ready for a maintainer.
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
                AttentionSection("releases", "Tool releases awaiting approval", releases(s)),
                AttentionSection("rollouts", "Tool rollouts waiting on you", rollouts(s)),
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

        /** Tool releases the toolkit proposes, oldest first. */
        internal fun releases(s: Snapshot): List<AttentionRow> =
            s.toolkit.rolloutsList.filter { it.stage == "proposed" }
                .sortedBy { it.started.instant }
                .map { r ->
                    AttentionRow(
                        id = "release:" + r.id,
                        title = "${r.tool} ${r.version}",
                        detail = if (r.from.isEmpty()) "new release" else "new release · the fleet runs ${r.from}",
                        url = r.notesUrl.ifEmpty { null },
                        updated = if (r.hasStarted()) r.started.instant else null,
                        approval = Approval.Release(r.id, r.tool, r.version),
                    )
                }

        /**
         * Tool rollouts that wait on you: a pin PR to merge (nothing goes
         * on until it does), then repos held for a look, refused, failed,
         * or failing CI, as the hub says, newest rollout first. A cancelled
         * rollout's don't: you stopped it. The row opens the PR when there
         * is one.
         */
        internal fun rollouts(s: Snapshot): List<AttentionRow> =
            s.toolkit.rolloutsList.filter { it.stage != "cancelled" && it.stage != "proposed" }
                .flatMap { r ->
                    val pin = if (!pinWaits(r)) emptyList() else listOf(
                        AttentionRow(
                            id = "rollout:${r.id} pin",
                            title = "Merge the ${r.tool} ${r.version} pin",
                            detail = listOf("pin PR open", prRef(r.pinPr)).filter { it.isNotEmpty() }.joinToString(" · "),
                            url = r.pinPr,
                            updated = if (r.hasUpdated()) r.updated.instant else null,
                        ),
                    )
                    pin + r.targetsList.filter { it.needsYou.isNotEmpty() }.map { t ->
                        val place = if (t.url.isEmpty()) t.branch.ifEmpty { t.detail } else ""
                        AttentionRow(
                            id = "rollout:${r.id} ${t.target}",
                            title = "${r.tool} ${r.version} on ${t.target}",
                            detail = listOf(t.needsYou, place).filter { it.isNotEmpty() }.joinToString(" · "),
                            url = t.url.ifEmpty { null },
                            updated = if (r.hasUpdated()) r.updated.instant else null,
                        )
                    }
                }

        /** Whether a rollout waits for you to merge its pin PR. */
        internal fun pinWaits(r: ToolkitRolloutState): Boolean =
            r.stage == "pin" && r.pinPr.isNotEmpty() && r.pinState != "MERGED" && r.pinState != "CLOSED"

        /** "owner/repo#N" for a GitHub PR URL, "" for anything else. */
        internal fun prRef(url: String): String {
            val p = Regex("""^https://github\.com/([^/]+)/([^/]+)/pull/(\d+)/?$""").find(url) ?: return ""
            return "${p.groupValues[1]}/${p.groupValues[2]}#${p.groupValues[3]}"
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
