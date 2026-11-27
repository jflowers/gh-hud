package io.github.jflowers.ghhud

import io.github.jflowers.ghhud.api.v1.CheckoutActionKind
import io.github.jflowers.ghhud.api.v1.CheckoutHealth
import io.github.jflowers.ghhud.api.v1.FleetAction
import io.github.jflowers.ghhud.api.v1.ToolkitAction
import io.github.jflowers.ghhud.api.v1.TranscriptResponse
import io.github.jflowers.ghhud.core.AgentChoice
import io.github.jflowers.ghhud.core.AgentRow
import io.github.jflowers.ghhud.core.Answer
import io.github.jflowers.ghhud.core.AttentionRow
import io.github.jflowers.ghhud.core.CaseRow
import io.github.jflowers.ghhud.core.EpicRow
import io.github.jflowers.ghhud.core.FleetSort
import io.github.jflowers.ghhud.core.HubAddress
import io.github.jflowers.ghhud.core.ItemRow
import io.github.jflowers.ghhud.core.PaneRead
import io.github.jflowers.ghhud.core.PlanItem
import io.github.jflowers.ghhud.core.RolloutRow
import io.github.jflowers.ghhud.core.WatchTarget

/** Actions that do nothing, for tests that drive the whole app screen. */
object NoopActions : AppActions {
    override fun open(url: String) {}
    override fun approve(row: AttentionRow) {}
    override fun reject(row: AttentionRow) {}
    override fun refresh() {}
    override fun setAddress(a: HubAddress) {}
    override fun setNotify(on: Boolean) {}
    override fun update(change: (ItemsView) -> ItemsView) {}
    override fun keepTerms() {}
    override fun copy(text: String) {}
    override suspend fun choices(row: ItemRow, queue: Boolean): Answer<List<AgentChoice>> = Answer.Ok(emptyList())
    override suspend fun run(row: ItemRow, c: AgentChoice, queue: Boolean): Answer<String> = Answer.Ok("")
    override suspend fun recheck(row: ItemRow): Answer<String> = Answer.Ok("")
    override suspend fun enqueue(r: PlanItem): Answer<String> = Answer.Ok("")
    override suspend fun move(r: PlanItem, by: Long): Answer<String> = Answer.Ok("")
    override suspend fun unqueue(r: PlanItem): Answer<String> = Answer.Ok("")
    override suspend fun approve(c: CaseRow): Answer<String> = Answer.Ok("")
    override suspend fun reject(c: CaseRow): Answer<String> = Answer.Ok("")
    override suspend fun approve(e: EpicRow): Answer<String> = Answer.Ok("")
    override suspend fun reread(e: EpicRow): Answer<String> = Answer.Ok("")
    override suspend fun drop(e: EpicRow): Answer<String> = Answer.Ok("")
    override suspend fun finish(e: EpicRow): Answer<String> = Answer.Ok("")
    override suspend fun settle(r: EpicRow.Replan, approve: Boolean): Answer<String> = Answer.Ok("")
    override suspend fun clearGate(epic: String, piece: String): Answer<String> = Answer.Ok("")
    override suspend fun enroll(target: String): Answer<String> = Answer.Ok("")
    override fun updateAgents(change: (AgentsView) -> AgentsView) {}
    override fun refreshAgents() {}
    override fun refreshWorkspaces() {}
    override suspend fun recheck(r: AgentRow): Answer<String> = Answer.Ok("")
    override suspend fun checkoutAction(h: CheckoutHealth, kind: CheckoutActionKind): Answer<String> = Answer.Ok("")
    override suspend fun fleet(action: FleetAction): Answer<String> = Answer.Ok("")
    override suspend fun dewey(machine: String?, force: Boolean): Answer<String> = Answer.Ok("")
    override suspend fun deweyCancel(machine: String?): Answer<String> = Answer.Ok("")
    override fun updateFleet(change: (ListState<FleetSort>) -> ListState<FleetSort>) {}
    override fun follow(t: WatchTarget) = kotlinx.coroutines.flow.emptyFlow<PaneRead>()
    override suspend fun transcript(runId: String): Answer<TranscriptResponse> = Answer.No("none")
    override suspend fun checkTools(): Answer<String> = Answer.Ok("")
    override suspend fun checkDrift(): Answer<String> = Answer.Ok("")
    override suspend fun fixDrift(target: String): Answer<String> = Answer.Ok("")
    override suspend fun rollOut(tool: String, version: String): Answer<String> = Answer.Ok("")
    override suspend fun decline(id: String): Answer<String> = Answer.Ok("")
    override suspend fun toolkit(action: ToolkitAction, r: RolloutRow): Answer<String> = Answer.Ok("")
}
