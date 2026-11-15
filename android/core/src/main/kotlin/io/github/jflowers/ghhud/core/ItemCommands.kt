package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.Run

// The commands on an item: start an agent, add one to your queue, re-check
// its last run. The hub does the work; these say how it went, in the Mac's
// words (macos/Sources/GHHud/ItemCommands.swift).

/** An agent task you can pick for an item. */
public data class AgentChoice(val task: String, val target: String, val cmd: String = "") {
    val id: String get() = "$task|$target"
}

public object ItemCommands {
    /** Why commands are off, if they are: no hub, or the hub says so. */
    public fun off(state: HubState): String? =
        if (state.status != HubStatus.Connected) "Not connected to the hub" else ItemList.commandsOff(state.snapshot)

    /** The tasks to offer: where one can start now, or what can be queued. */
    public suspend fun choices(hub: HubConnection, row: ItemRow, queue: Boolean): Answer<List<AgentChoice>> {
        if (queue) {
            return when (val a = hub.queueChoices(row.id)) {
                is Answer.No -> Answer.No("Can't queue: ${a.why}")
                is Answer.Ok ->
                    if (a.value.isEmpty()) {
                        Answer.No("No agent tasks to queue for this item.")
                    } else {
                        Answer.Ok(a.value.map { AgentChoice(it, "your queue") })
                    }
            }
        }
        return when (val a = hub.menuChoices(row.id)) {
            is Answer.No -> Answer.No("The agent menu failed: ${a.why}")
            is Answer.Ok ->
                if (a.value.isEmpty()) {
                    Answer.No("Nothing can start on this item now: no agent task fits it, or no checkout of ${row.repo} is free.")
                } else {
                    Answer.Ok(a.value.map { AgentChoice(it.task, it.target, it.cmd) })
                }
        }
    }

    /** Starts or queues a choice; the answer is what to say. */
    public suspend fun run(hub: HubConnection, row: ItemRow, c: AgentChoice, queue: Boolean): Answer<String> {
        if (queue) {
            return when (val a = hub.enqueue(c.task, row.id)) {
                is Answer.No -> a
                is Answer.Ok -> Answer.Ok("Queued ${c.task} for ${row.ref}, number ${a.value} in your queue")
            }
        }
        return when (val a = hub.dispatch(c.task, row.id)) {
            is Answer.No -> a
            is Answer.Ok -> {
                val rec = a.value
                val at = if (rec.workspace.isEmpty()) "" else " in ${rec.machine.ifEmpty { "local" }}:${rec.workspace}"
                Answer.Ok("Started ${rec.task.ifEmpty { c.task }} on ${row.ref}$at")
            }
        }
    }

    /** Reads the item's last run's outcome again; the answer is what to say. */
    public suspend fun recheck(hub: HubConnection, row: ItemRow): Answer<String> {
        val run = row.latestRun ?: return Answer.No("No run to re-check")
        return when (val a = hub.reinspect(run.id)) {
            is Answer.No -> Answer.No("Can't re-check ${run.task} on ${row.ref}: ${a.why}")
            is Answer.Ok -> Answer.Ok("Re-checked ${run.task} on ${row.ref}: ${outcome(a.value)}")
        }
    }

    /** A run's outcome in words, with its summary. */
    public fun outcome(r: Run): String {
        val words = RunTag.words(r.status)
        return if (r.summary.isEmpty()) words else "$words — ${r.summary}"
    }
}
