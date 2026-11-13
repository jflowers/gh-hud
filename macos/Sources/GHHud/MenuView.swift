import AppKit
import GHHudKit
import SwiftUI

/// The window under the menu bar icon.
struct MenuView: View {
    let conn: HubConnection
    /// Lays the menu out for ImageRenderer (--snapshot), which can't draw
    /// scroll views or system buttons.
    var rendering = false
    // Not @State (a macro only Xcode ships): see GHHudApp.
    @StateObject private var ui = MenuState()

    /// Rows shown per section before "and N more".
    private let perSection = 8

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            header
            Divider()
            content
            Divider()
            footer
        }
        .padding(12)
        .frame(width: 400)
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text("gh-hud").font(.headline)
            Text(statusLine)
                .font(.caption)
                .foregroundStyle(statusIsBad ? .red : .secondary)
                .lineLimit(2)
            if conn.status == .connected {
                ForEach(conn.snapshot.configWarnings, id: \.self) { w in
                    Label(w, systemImage: "exclamationmark.triangle")
                        .font(.caption)
                        .foregroundStyle(.orange)
                        .lineLimit(3)
                }
            }
        }
    }

    private var statusLine: String {
        switch conn.status {
        case .connecting: return "connecting to the hub…"
        case .connected: return HubSummary.line(conn.snapshot)
        case .noHub(let why): return why
        case .lost(let why): return why
        }
    }

    private var statusIsBad: Bool {
        switch conn.status {
        case .noHub, .lost: return true
        case .connected: return !conn.snapshot.fetchError.isEmpty
        case .connecting: return false
        }
    }

    @ViewBuilder
    private var content: some View {
        if conn.status != .connected {
            EmptyView()
        } else if conn.attention.sections.isEmpty {
            Text("Nothing needs you right now.")
                .foregroundStyle(.secondary)
        } else if rendering {
            sectionList
        } else {
            ScrollView { sectionList }
                .frame(maxHeight: 460)
        }
    }

    private var sectionList: some View {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(conn.attention.sections) { section in
                        VStack(alignment: .leading, spacing: 2) {
                            Text("\(section.title) (\(section.rows.count))")
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(.secondary)
                            ForEach(section.rows.prefix(perSection)) { row in
                                if rendering {
                                    RowLabel(row: row, highlighted: false)
                                    if let a = row.approval {
                                        Text(a.rejectable ? "Approve · Reject" : "Approve")
                                            .font(.caption).foregroundStyle(.tint)
                                            .padding(.horizontal, 6)
                                    }
                                } else {
                                    RowButton(row: row, ui: ui)
                                    if let a = row.approval {
                                        ApprovalButtons(row: row, approval: a, conn: conn, ui: ui)
                                    }
                                }
                            }
                            if section.rows.count > perSection {
                                Text("and \(section.rows.count - perSection) more in the dashboard")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }
                }
    }

    @ViewBuilder
    private var footer: some View {
        if rendering {
            HStack {
                Text("Refresh")
                Spacer()
                Text("Quit")
            }
            .foregroundStyle(.secondary)
        } else {
            buttons
        }
    }

    private var buttons: some View {
        HStack {
            Button("Refresh") {
                Task { ui.refreshError = await conn.refresh() }
            }
            .disabled(conn.status != .connected)
            if let refreshError = ui.refreshError {
                Text(refreshError).font(.caption).foregroundStyle(.red).lineLimit(1)
            }
            Spacer()
            Button("Quit") { NSApp.terminate(nil) }
        }
    }
}

/// The menu's own state: the hovered row, the last refresh error, and
/// approvals in flight or failed, by row.
@MainActor
final class MenuState: ObservableObject {
    @Published var hovered: String?
    @Published var refreshError: String?
    @Published var busy: Set<String> = []
    @Published var actionError: [String: String] = [:]
}

/// Approve (and, for a proposal, Reject) under a row. The row goes away
/// when the hub's next snapshot no longer has it waiting.
struct ApprovalButtons: View {
    let row: AttentionRow
    let approval: Approval
    let conn: HubConnection
    @ObservedObject var ui: MenuState

    var body: some View {
        HStack(spacing: 8) {
            Button("Approve") { run { await conn.approve(approval) } }
            if approval.rejectable {
                Button("Reject") { run { await conn.reject(approval) } }
            }
            if ui.busy.contains(row.id) {
                ProgressView().controlSize(.small)
            } else if let err = ui.actionError[row.id] {
                Text(err).font(.caption).foregroundStyle(.red).lineLimit(2)
            }
        }
        .controlSize(.small)
        .disabled(ui.busy.contains(row.id) || conn.status != .connected)
        .padding(.horizontal, 6)
        .padding(.bottom, 4)
    }

    private func run(_ act: @escaping @MainActor () async -> String?) {
        let id = row.id
        ui.busy.insert(id)
        ui.actionError[id] = nil
        Task {
            let err = await act()
            ui.busy.remove(id)
            ui.actionError[id] = err
        }
    }
}

/// A clickable row: opens the item on GitHub.
struct RowButton: View {
    let row: AttentionRow
    @ObservedObject var ui: MenuState
    private var hovering: Bool { ui.hovered == row.id }

    var body: some View {
        Button {
            if let url = row.url { NSWorkspace.shared.open(url) }
        } label: {
            RowLabel(row: row, highlighted: hovering)
        }
        .buttonStyle(.plain)
        .onHover { inside in
            if inside {
                ui.hovered = row.id
            } else if ui.hovered == row.id {
                ui.hovered = nil
            }
        }
        .disabled(row.url == nil)
        .help(row.url?.absoluteString ?? "")
    }
}

/// A row's title and detail.
struct RowLabel: View {
    let row: AttentionRow
    let highlighted: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(row.title).lineLimit(1)
            Text(row.detail).font(.caption).foregroundStyle(.secondary).lineLimit(1)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.vertical, 3)
        .padding(.horizontal, 6)
        .background(highlighted ? Color.accentColor.opacity(0.15) : .clear,
                    in: RoundedRectangle(cornerRadius: 5))
        .contentShape(Rectangle())
    }
}
