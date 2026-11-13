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
                                } else {
                                    RowButton(row: row, ui: ui)
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

/// The menu's own state: the hovered row and the last refresh error.
@MainActor
final class MenuState: ObservableObject {
    @Published var hovered: String?
    @Published var refreshError: String?
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
