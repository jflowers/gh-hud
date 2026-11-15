import GHHudKit
import SwiftUI

// The Fleet pane: the TUI's F view of the GitOps rollout. The target and
// its CI above, a machine each below.

struct FleetPane: View {
    let conn: HubConnection
    @ObservedObject var ui: DashboardState

    var body: some View {
        let f = conn.snapshot.fleet
        let rows = TableFilter.rows(FleetList.rows(conn.snapshot), query: ui.fleetQuery, kept: ui.fleetFilters)
            .sorted(using: ui.fleetSort)
        VStack(alignment: .leading, spacing: 0) {
            header(f)
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
            Divider()
            Table(rows, sortOrder: $ui.fleetSort) {
                TableColumn("", value: \.stateRank) { r in StatusGlyph(tone: r.tone, active: r.state != "pending") }
                    .width(12)
                TableColumn("Machine", value: \.name) { r in Text(r.name) }
                    .width(min: 60, ideal: 110, max: 200)
                TableColumn("Reached", value: \.reach) { r in
                    Text(r.reach).foregroundStyle(r.reach == "no" ? Color.orange : .secondary)
                }
                .width(min: 50, ideal: 64, max: 90)
                TableColumn("Slots", value: \.slotsUsed) { r in Text(r.slots).foregroundStyle(.secondary).monospacedDigit() }
                    .width(min: 36, ideal: 48, max: 70)
                TableColumn("Applied", value: \.applied) { r in Text(r.applied).font(.body.monospaced()).lineLimit(1) }
                    .width(min: 80, ideal: 170, max: 260)
                TableColumn("State", value: \.stateRank) { r in Text(r.state).foregroundStyle(r.tone.color) }
                    .width(min: 60, ideal: 90, max: 130)
                TableColumn("Waited", value: \.waitedKey) { r in
                    Text(r.waited.map(AgentList.duration) ?? "").foregroundStyle(.secondary).monospacedDigit()
                }
                .width(min: 40, ideal: 56, max: 80)
                TableColumn("Last error", value: \.error) { r in
                    Text(r.error).foregroundStyle(.red).lineLimit(1).help(r.error)
                }
                .width(min: 100, ideal: 280)
            }
            .contextMenu(forSelectionType: FleetRow.ID.self) { ids in
                if let r = rows.first(where: { ids.contains($0.id) }) {
                    FilterMenu(row: r, filters: $ui.fleetFilters)
                }
                HubOrderButton(sort: $ui.fleetSort)
            }
            .safeAreaInset(edge: .top, spacing: 0) { FilterBar(filters: $ui.fleetFilters) }
            .tableSearch(text: $ui.fleetQuery, kept: $ui.fleetFilters, columns: FleetRow.filterColumns,
                         prompt: "Search or state:failed")
            .overlay {
                if rows.isEmpty, !ui.fleetQuery.isEmpty || !ui.fleetFilters.isEmpty {
                    NoMatches(query: ui.fleetQuery)
                }
            }
        }
        .confirmationDialog("Skip the drain wait?", isPresented: $ui.confirmSkipWait) {
            Button("Skip Wait", role: .destructive) { control(.skipWait, "Applying without waiting for agents to finish") }
        } message: {
            Text("The machine being updated applies the new commit now, without waiting for its running agents to finish. They may be interrupted.")
        }
    }

    @ViewBuilder
    private func header(_ f: Ghhud_V1_FleetSnapshot) -> some View {
        let (state, tone) = FleetList.state(f)
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text(state).font(.title3.weight(.semibold)).foregroundStyle(tone.color)
                if f.targetShort.isEmpty {
                    Text("no target yet").foregroundStyle(.secondary)
                } else {
                    Text("target").foregroundStyle(.secondary)
                    Text(f.targetShort).font(.body.monospaced()).textSelection(.enabled).help(f.target)
                }
                Text("CI").foregroundStyle(.secondary)
                Text(f.ci.isEmpty ? "unknown" : f.ci).foregroundStyle(FleetList.ciTone(f.ci).color)
                if !f.ciDetail.isEmpty {
                    Text(f.ciDetail).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            if !f.banner.isEmpty {
                Label(f.banner, systemImage: "exclamationmark.triangle.fill").foregroundStyle(.yellow)
            }
            if !f.lastError.isEmpty {
                Label(f.lastError, systemImage: "xmark.octagon.fill").foregroundStyle(.red).textSelection(.enabled)
            }
        }
    }

    func control(_ a: Ghhud_V1_FleetAction, _ said: String) {
        FleetTools.run(conn: conn, ui: ui, a, said)
    }
}

/// The fleet's toolbar: the TUI's p, a, s and r.
struct FleetTools: ToolbarContent {
    let conn: HubConnection
    @ObservedObject var ui: DashboardState

    var body: some ToolbarContent {
        let f = conn.snapshot.fleet
        let off = conn.status != .connected || !conn.snapshot.hasFleet
        ToolbarItemGroup {
            Button {
                Self.run(conn: conn, ui: ui, f.paused ? .resume : .pause, f.paused ? "Resumed the rollout" : "Paused the rollout")
            } label: {
                Label(f.paused ? "Resume" : "Pause", systemImage: f.paused ? "play.fill" : "pause.fill")
            }
            .help(f.paused ? "Resume the rollout" : "Pause the rollout: no machine starts applying")
            .disabled(off)
            Button {
                Self.run(conn: conn, ui: ui, .applyNow, "Checking the target now")
            } label: {
                Label("Apply Now", systemImage: "arrow.down.circle")
            }
            .help("Check the target now instead of at the next poll")
            .disabled(off)
            Button {
                ui.confirmSkipWait = true
            } label: {
                Label("Skip Wait", systemImage: "forward.fill")
            }
            .help("Apply without waiting for agents to drain")
            .disabled(off || !f.machines.contains { $0.state == "draining" })
            Button {
                Self.run(conn: conn, ui: ui, .retry, "Retrying \(f.failed)")
            } label: {
                Label("Retry", systemImage: "arrow.counterclockwise")
            }
            .help(f.failed.isEmpty ? "Nothing failed" : "Retry \(f.failed)")
            .disabled(off || f.failed.isEmpty)
        }
    }

    @MainActor
    static func run(conn: HubConnection, ui: DashboardState, _ a: Ghhud_V1_FleetAction, _ said: String) {
        Task {
            if let why = await conn.fleet(a) {
                ui.say("Fleet: \(why)", bad: true)
            } else {
                ui.say(said)
            }
        }
    }
}
