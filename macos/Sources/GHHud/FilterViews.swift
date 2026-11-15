import GHHudKit
import SwiftUI

// The tables' column filters: the chips above a table, the Show Only and
// Hide submenus in a row's context menu, and the search field, where Return
// keeps the column terms typed (`repo:acme`) as chips.

/// The kept filters as chips; click a chip's ✕ to drop it.
struct FilterBar: View {
    @Binding var filters: [ColumnFilter]

    var body: some View {
        if !filters.isEmpty {
            HStack(spacing: 6) {
                Image(systemName: "line.3.horizontal.decrease.circle")
                    .foregroundStyle(.secondary)
                    .help("Showing only rows that pass these filters")
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(filters) { f in
                            FilterChip(filter: f) { filters.removeAll { $0 == f } }
                        }
                    }
                }
                Button("Clear") { filters = [] }
                    .buttonStyle(.link)
                    .font(.callout)
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            .background(.bar)
            .overlay(alignment: .bottom) { Divider() }
        }
    }
}

struct FilterChip: View {
    let filter: ColumnFilter
    let remove: () -> Void

    var body: some View {
        HStack(spacing: 4) {
            Text(filter.exclude ? "not \(filter.column)" : filter.column)
                .foregroundStyle(.secondary)
            Text(filter.value).lineLimit(1)
            Button(action: remove) {
                Image(systemName: "xmark.circle.fill")
            }
            .buttonStyle(.plain)
            .foregroundStyle(.secondary)
            .help("Remove this filter")
        }
        .font(.callout)
        .padding(.leading, 8)
        .padding(.trailing, 5)
        .padding(.vertical, 2)
        .background(Capsule().fill(filter.exclude ? Color.red.opacity(0.18) : Color.accentColor.opacity(0.2)))
        .help(filter.term)
    }
}

/// "Show Only" and "Hide" submenus offering the row's value in each
/// column; `except` leaves out columns whose values are too long to be
/// worth a menu item (they can still be typed).
struct FilterMenu<Row: ColumnFilterable>: View {
    let row: Row
    @Binding var filters: [ColumnFilter]
    var except: Set<String> = []

    var body: some View {
        let choices = row.filterChoices.filter { !except.contains($0.column) }
        if !choices.isEmpty {
            Divider()
            Menu("Show Only") {
                ForEach(Array(choices.enumerated()), id: \.offset) { _, c in
                    Button("\(c.column): \(c.value)") { filters.add(ColumnFilter(c.column, c.value)) }
                }
            }
            Menu("Hide") {
                ForEach(Array(choices.enumerated()), id: \.offset) { _, c in
                    Button("\(c.column): \(c.value)") { filters.add(ColumnFilter(c.column, c.value, exclude: true)) }
                }
            }
        }
    }
}

/// The empty state when filters or a search leave nothing.
struct NoMatches: View {
    let query: String

    var body: some View {
        if query.isEmpty {
            ContentUnavailableView("No matches", systemImage: "line.3.horizontal.decrease.circle",
                                   description: Text("No rows pass the filters."))
        } else {
            ContentUnavailableView.search(text: query)
        }
    }
}

extension View {
    /// The toolbar's search field for a table: it filters as you type, and
    /// Return keeps its column terms as chips.
    func tableSearch(text: Binding<String>, kept: Binding<[ColumnFilter]>, columns: [String],
                     prompt: String) -> some View {
        searchable(text: text, placement: .toolbar, prompt: prompt)
            .onSubmit(of: .search) {
                text.wrappedValue = kept.wrappedValue.keepTerms(of: text.wrappedValue, columns: columns)
            }
    }
}
