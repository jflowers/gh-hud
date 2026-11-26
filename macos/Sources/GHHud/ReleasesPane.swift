import AppKit
import GHHudKit
import SwiftUI

// The Releases pane (#218): every repo jflowers/ship releases, from its
// repos.yaml, with the latest release, the commits since it, the ship
// version it's on and its last release run. Double-click opens the
// release; the right-click menu opens its unreleased commits, last run or
// repo, or copies the `gh ship release` command.

struct ReleasesPane: View {
    let conn: HubConnection
    @ObservedObject var ui: DashboardState

    var body: some View {
        let now = Date()
        let rows = ReleaseList.rows(conn.snapshot, now: now)
        VStack(alignment: .leading, spacing: 0) {
            header(now: now)
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
            Divider()
            Table(rows, selection: $ui.releaseSelection) {
                TableColumn("") { r in StatusGlyph(tone: r.runTone, active: !r.run.isEmpty) }
                    .width(12)
                TableColumn("Repo") { r in
                    Text(r.repo).help(r.kinds.isEmpty ? r.repo : "\(r.repo): \(r.kinds)")
                }
                .width(min: 140, ideal: 240)
                TableColumn("Latest") { r in Text(r.latest).font(.body.monospaced()) }
                    .width(min: 60, ideal: 80, max: 120)
                TableColumn("Released") { r in Text(r.released).foregroundStyle(.secondary) }
                    .width(min: 50, ideal: 60, max: 80)
                TableColumn("Unreleased") { r in
                    Text(r.unreleased).font(.body.monospacedDigit()).foregroundStyle(r.unreleasedTone.color)
                        .help(r.hasUnreleased ? "\(r.unreleased) commits since \(r.latest)" : "")
                }
                .width(min: 60, ideal: 75, max: 90)
                TableColumn("Ship") { r in
                    Text(r.ship).font(.body.monospaced()).foregroundStyle(.secondary)
                        .help("The ship version repos.yaml records")
                }
                .width(min: 60, ideal: 75, max: 100)
                TableColumn("Last Run") { r in
                    if !r.error.isEmpty {
                        Text(r.error).foregroundStyle(.red).lineLimit(1).help(r.error)
                    } else {
                        Text(r.run).foregroundStyle(r.runTone.color)
                    }
                }
                .width(min: 90, ideal: 140)
            }
            .contextMenu(forSelectionType: ReleaseRow.ID.self) { ids in
                if let r = rows.first(where: { ids.contains($0.id) }) {
                    menu(r)
                }
            } primaryAction: { ids in
                if let r = rows.first(where: { ids.contains($0.id) }), let url = r.releaseURL ?? r.repoURL {
                    NSWorkspace.shared.open(url)
                }
            }
            .overlay {
                if rows.isEmpty {
                    ContentUnavailableView(conn.snapshot.releases.checking ? "Reading repos.yaml…" : "No repos",
                                           systemImage: "shippingbox")
                }
            }
        }
    }

    @ViewBuilder
    private func header(now: Date) -> some View {
        let r = conn.snapshot.releases
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text("Releases").font(.title3.weight(.semibold))
            Text(ReleaseList.state(conn.snapshot, now: now))
                .foregroundStyle(r.error.isEmpty ? Color.secondary : .red)
                .textSelection(.enabled)
            Spacer()
            if !r.manifest.isEmpty, let url = URL(string: "https://github.com/\(Self.blobPath(r.manifest))") {
                Link("repos.yaml", destination: url).help(r.manifest)
            }
        }
    }

    @ViewBuilder
    private func menu(_ r: ReleaseRow) -> some View {
        if let url = r.releaseURL {
            Button("Open Release \(r.latest)") { NSWorkspace.shared.open(url) }
        }
        if let url = r.compareURL {
            Button("Open Unreleased Commits (\(r.unreleased))") { NSWorkspace.shared.open(url) }
        }
        if let url = r.runURL {
            Button("Open Last Release Run") { NSWorkspace.shared.open(url) }
        }
        if let url = r.repoURL {
            Button("Open Repository") { NSWorkspace.shared.open(url) }
        }
        Divider()
        Button("Copy Release Command") {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(r.releaseCommand, forType: .string)
        }
        .help("gh ship release \(r.repo) <tag>")
    }

    /// "jflowers/ship/repos.yaml" → "jflowers/ship/blob/HEAD/repos.yaml".
    static func blobPath(_ manifest: String) -> String {
        let parts = manifest.split(separator: "/", maxSplits: 2).map(String.init)
        guard parts.count == 3 else { return manifest }
        return "\(parts[0])/\(parts[1])/blob/HEAD/\(parts[2])"
    }
}
