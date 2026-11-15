import Foundation

// Column filters for the tables: "show only repo acme/r", "hide author
// dependabot". They come from a row's context menu (kept as chips above
// the table) or from column terms typed in the search field,
// `repo:acme -author:dependabot "label:good first issue"`. Filters on one
// column are alternatives (repo a or repo b); filters on different columns
// all apply; a hide filter drops every row it matches. Values match
// ignoring case, anywhere in the cell: `repo:widgets` finds acme/widgets.

/// One filter on one column.
public struct ColumnFilter: Hashable, Identifiable, Sendable {
    public let column: String
    public let value: String
    /// Hide the rows it matches, rather than show only them.
    public let exclude: Bool

    public init(_ column: String, _ value: String, exclude: Bool = false) {
        self.column = column.lowercased()
        self.value = value
        self.exclude = exclude
    }

    public var id: String { term }

    /// How it's typed in the search field: `-repo:acme/r`, quoted when
    /// the value has a space.
    public var term: String {
        let v = value.contains(" ") ? "\"\(value)\"" : value
        return (exclude ? "-" : "") + column + ":" + v
    }

    func matches(_ values: [String]) -> Bool {
        let v = value.lowercased()
        return values.contains { $0.lowercased().contains(v) }
    }
}

/// A row a table can filter by column.
public protocol ColumnFilterable {
    /// The columns, in menu order, as they're typed (`repo`, `kind`).
    static var filterColumns: [String] { get }
    /// The row's values in a column: none, one, or several (labels).
    func filterValues(_ column: String) -> [String]
}

extension ColumnFilterable {
    /// Every filter applies, as described above.
    public func passes(_ filters: [ColumnFilter]) -> Bool {
        var shown: [String: Bool] = [:]
        for f in filters {
            let hit = f.matches(filterValues(f.column))
            if f.exclude {
                if hit { return false }
            } else {
                shown[f.column] = (shown[f.column] ?? false) || hit
            }
        }
        return !shown.values.contains(false)
    }

    /// Free text: any column holds it.
    public func contains(_ text: String) -> Bool {
        let t = text.lowercased()
        return t.isEmpty || Self.filterColumns.contains { c in
            filterValues(c).contains { $0.lowercased().contains(t) }
        }
    }

    /// The filters a row's context menu offers: a value of each column it
    /// has one in.
    public var filterChoices: [(column: String, value: String)] {
        Self.filterColumns.flatMap { c in
            filterValues(c).filter { !$0.isEmpty }.map { (c, $0) }
        }
    }
}

/// A search field's text: the column terms in it, and the rest.
public struct TableQuery: Equatable, Sendable {
    public let filters: [ColumnFilter]
    public let text: String

    /// Splits on spaces, keeping "quoted words" together. A term is
    /// `column:value` or `-column:value` with a column the table has;
    /// anything else (`#12`, `http://…`, `foo:bar`) stays text.
    public static func parse(_ q: String, columns: [String]) -> TableQuery {
        var filters: [ColumnFilter] = []
        var text: [String] = []
        for word in words(q) {
            let exclude = word.hasPrefix("-")
            let body = exclude ? String(word.dropFirst()) : word
            if let colon = body.firstIndex(of: ":") {
                let column = body[..<colon].lowercased()
                let value = String(body[body.index(after: colon)...])
                if columns.contains(column), !value.isEmpty {
                    filters.append(ColumnFilter(column, value, exclude: exclude))
                    continue
                }
            }
            text.append(word)
        }
        return TableQuery(filters: filters, text: text.joined(separator: " "))
    }

    /// Words, with quotes grouping and removed: `label:"good first"` is
    /// one word, `label:good first`.
    static func words(_ q: String) -> [String] {
        var out: [String] = []
        var cur = ""
        var quoted = false
        var started = false
        for ch in q {
            if ch == "\"" {
                quoted.toggle()
                started = true
            } else if ch.isWhitespace, !quoted {
                if started { out.append(cur) }
                cur = ""
                started = false
            } else {
                cur.append(ch)
                started = true
            }
        }
        if started { out.append(cur) }
        return out.filter { !$0.isEmpty }
    }
}

extension Array where Element == ColumnFilter {
    /// Adds a filter once.
    public mutating func add(_ f: ColumnFilter) {
        if !contains(f) { append(f) }
    }
}

// MARK: the tables' columns

extension ItemRow: ColumnFilterable {
    public static let filterColumns = ["kind", "status", "repo", "title", "author", "last", "label"]

    public func filterValues(_ column: String) -> [String] {
        switch column {
        case "kind": return [kind]
        case "status": return statusWords
        case "repo": return [repo]
        case "title": return [title]
        case "author": return author.isEmpty ? [] : [author]
        case "last": return lastActor.isEmpty ? [] : [lastActor]
        case "label": return labels
        default: return []
        }
    }

    /// The status cluster in words: "ci failing", "approved", "conflict".
    public var statusWords: [String] {
        var w: [String] = []
        switch ci {
        case .good: w.append("ci passing")
        case .bad: w.append("ci failing")
        case .pending: w.append("ci pending")
        case .muted: w.append("no checks")
        default: break
        }
        switch review {
        case .good: w.append("approved")
        case .bad: w.append("changes requested")
        case .warn: w.append("review required")
        case .pending: w.append("review pending")
        default: break
        }
        if conflict { w.append("conflict") }
        if draft { w.append("draft") }
        return w
    }
}

extension AgentRow: ColumnFilterable {
    public static let filterColumns = ["task", "agent", "where", "workspace", "status"]

    public func filterValues(_ column: String) -> [String] {
        switch column {
        case "task": return task.isEmpty ? [] : [task]
        case "agent": return [name]
        case "where": return [machine]
        case "workspace": return workspace.isEmpty ? [] : [workspace]
        case "status": return status.isEmpty ? [] : [status]
        default: return []
        }
    }
}

extension WorkspaceRow: ColumnFilterable {
    public static let filterColumns = ["where", "workspace", "repo", "branch", "state"]

    public func filterValues(_ column: String) -> [String] {
        switch column {
        case "where": return [machine]
        case "workspace": return [workspace]
        case "repo": return [repo]
        case "branch": return branch.isEmpty ? [] : [branch]
        case "state": return [state]
        default: return []
        }
    }
}

extension FleetRow: ColumnFilterable {
    public static let filterColumns = ["machine", "reached", "state"]

    public func filterValues(_ column: String) -> [String] {
        switch column {
        case "machine": return [name]
        case "reached": return [reach]
        case "state": return [state]
        default: return []
        }
    }
}

/// A table's rows through its search field and kept filters.
public enum TableFilter {
    /// The rows the kept filters and the search's terms and text pass;
    /// free text matches any column.
    public static func rows<R: ColumnFilterable>(_ rows: [R], query: String, kept: [ColumnFilter]) -> [R] {
        let q = TableQuery.parse(query, columns: R.filterColumns)
        let all = kept + q.filters
        let text = q.text.trimmingCharacters(in: .whitespaces)
        return rows.filter { $0.passes(all) && $0.contains(text) }
    }
}

extension Array where Element == ColumnFilter {
    /// Keeps a search's column terms as filters, and gives back the text
    /// left over: what Return in the search field does.
    public mutating func keepTerms(of query: String, columns: [String]) -> String {
        let q = TableQuery.parse(query, columns: columns)
        q.filters.forEach { add($0) }
        return q.text
    }
}
