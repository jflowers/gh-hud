package io.github.jflowers.ghhud.core

// The Plan's commands: your queue, cases, epics and their gates. The hub
// does the work; these say how it went, in the Mac's words
// (macos/Sources/GHHud/PlanPane.swift). An answer of "" has nothing to say.

public class PlanCommands(private val hub: HubConnection) {
    private fun <T> say(a: Answer<T>, ok: (T) -> String, no: (String) -> String): Answer<String> = when (a) {
        is Answer.Ok -> Answer.Ok(ok(a.value))
        is Answer.No -> Answer.No(no(a.why))
    }

    private fun why(s: String?): Answer<Unit> = if (s == null) Answer.Ok(Unit) else Answer.No(s)

    /** Queues an auto row's task. */
    public suspend fun enqueue(r: PlanItem): Answer<String> = say(
        hub.enqueue(r.task, r.item.url),
        { "Queued ${r.task} for ${r.ref}, number $it in your queue" },
        { "Can't queue ${r.task} for ${r.ref}: $it" },
    )

    /** Moves a queued task earlier (negative) or later; says nothing when it works. */
    public suspend fun move(r: PlanItem, by: Long): Answer<String> =
        say(hub.moveQueued(r.task, r.ref, by), { "" }, { "Can't move ${r.task} ${r.ref}: $it" })

    public suspend fun unqueue(r: PlanItem): Answer<String> = say(
        hub.unqueue(r.task, r.ref),
        { "Took ${r.task} ${r.ref} off your queue" },
        { "Can't unqueue ${r.task} ${r.ref}: $it" },
    )

    public suspend fun approve(c: CaseRow): Answer<String> {
        val id = c.proposal ?: return Answer.No("Nothing to approve for ${c.title}")
        return say(why(hub.approve(Approval.Proposal(id))), { "Approved: ${c.summary}" }, {
            "Can't approve the proposal for ${c.title}: $it"
        })
    }

    public suspend fun reject(c: CaseRow): Answer<String> {
        val id = c.proposal ?: return Answer.No("Nothing to reject for ${c.title}")
        return say(why(hub.reject(Approval.Proposal(id))), { "Rejected; ${c.title} stays with you" }, {
            "Can't reject the proposal for ${c.title}: $it"
        })
    }

    public suspend fun approve(e: EpicRow): Answer<String> {
        val plan = e.planToApprove ?: return Answer.No("No plan of ${e.ref} waits for you")
        return say(why(hub.approve(Approval.Epic(e.ref, plan))), { "Approved the plan for ${e.ref}" }, {
            "Can't approve the plan for ${e.ref}: $it"
        })
    }

    public suspend fun reread(e: EpicRow): Answer<String> =
        say(hub.rereadEpic(e.ref), { "Reading ${e.ref} again" }, { "Can't reread ${e.ref}: $it" })

    public suspend fun finish(e: EpicRow): Answer<String> {
        val plan = e.planToFinish ?: return Answer.No("${e.ref} isn't finished")
        return say(hub.finishEpic(e.ref, plan), { "Posted the summary and closed ${e.ref}" }, { "Can't finish ${e.ref}: $it" })
    }

    public suspend fun drop(e: EpicRow): Answer<String> =
        say(hub.dropEpic(e.ref), { "Dropped ${e.ref}" }, { "Can't drop ${e.ref}: $it" })

    public suspend fun clearGate(epic: String, piece: String): Answer<String> = say(
        why(hub.approve(Approval.Gate(epic, piece))),
        { "Cleared the gate on $piece" },
        { "Can't clear the gate on $piece: $it" },
    )

    /** Enrolls an epic: an issue URL or owner/repo#N. */
    public suspend fun enroll(target: String): Answer<String> =
        say(hub.enrollEpic(target.trim()), { "Enrolled $it; its plan shows here once it's read" }, { it })
}
