package io.github.jflowers.ghhud.core

// Column filters for the lists: "show only repo acme/r", "hide author
// dependabot". They come from a row's long-press menu (kept as chips) or
// from column terms typed in the search field,
// `repo:acme -author:dependabot "label:good first issue"`. Filters on one
// column are alternatives (repo a or repo b); filters on different columns
// all apply; a hide filter drops every row it matches. Values match
// ignoring case, anywhere in the cell: `repo:widgets` finds acme/widgets.
// A port of the Mac's Filters.swift.

/** One filter on one column. */
public data class ColumnFilter(val column: String, val value: String, val exclude: Boolean = false) {
    /**
     * How it's typed in the search field: `-repo:acme/r`, quoted when the
     * value has a space.
     */
    val term: String get() {
        val v = if (value.contains(" ")) "\"$value\"" else value
        return (if (exclude) "-" else "") + column + ":" + v
    }

    internal fun matches(values: List<String>): Boolean {
        val v = value.lowercase()
        return values.any { it.lowercase().contains(v) }
    }

    public companion object {
        /** A filter, its column lowercased as typed. */
        public fun of(column: String, value: String, exclude: Boolean = false): ColumnFilter =
            ColumnFilter(column.lowercase(), value, exclude)
    }
}

/** A row a list can filter by column. */
public interface ColumnFilterable {
    /** The columns, in menu order, as they're typed (`repo`, `kind`). */
    public val filterColumns: List<String>

    /** The row's values in a column: none, one, or several (labels). */
    public fun filterValues(column: String): List<String>
}

/** Every filter applies, as described above. */
public fun ColumnFilterable.passes(filters: List<ColumnFilter>): Boolean {
    val shown = LinkedHashMap<String, Boolean>()
    for (f in filters) {
        val hit = f.matches(filterValues(f.column))
        if (f.exclude) {
            if (hit) return false
        } else {
            shown[f.column] = (shown[f.column] ?: false) || hit
        }
    }
    return !shown.containsValue(false)
}

/** Free text: any column holds it. */
public fun ColumnFilterable.holds(text: String): Boolean {
    val t = text.lowercase()
    return t.isEmpty() || filterColumns.any { c -> filterValues(c).any { it.lowercase().contains(t) } }
}

/** The filters a row's menu offers: a value of each column it has one in. */
public val ColumnFilterable.filterChoices: List<Pair<String, String>>
    get() = filterColumns.flatMap { c -> filterValues(c).filter { it.isNotEmpty() }.map { c to it } }

/** A search field's text: the column terms in it, and the rest. */
public data class TableQuery(val filters: List<ColumnFilter>, val text: String) {
    public companion object {
        /**
         * Splits on spaces, keeping "quoted words" together. A term is
         * `column:value` or `-column:value` with a column the list has;
         * anything else (`#12`, `http://…`, `foo:bar`) stays text.
         */
        public fun parse(q: String, columns: List<String>): TableQuery {
            val filters = mutableListOf<ColumnFilter>()
            val text = mutableListOf<String>()
            for (word in words(q)) {
                val exclude = word.startsWith("-")
                val body = if (exclude) word.drop(1) else word
                val colon = body.indexOf(':')
                if (colon >= 0) {
                    val column = body.substring(0, colon).lowercase()
                    val value = body.substring(colon + 1)
                    if (column in columns && value.isNotEmpty()) {
                        filters += ColumnFilter.of(column, value, exclude)
                        continue
                    }
                }
                text += word
            }
            return TableQuery(filters, text.joinToString(" "))
        }

        /**
         * Words, with quotes grouping and removed: `label:"good first"` is
         * one word, `label:good first`.
         */
        internal fun words(q: String): List<String> {
            val out = mutableListOf<String>()
            val cur = StringBuilder()
            var quoted = false
            var started = false
            for (ch in q) {
                if (ch == '"') {
                    quoted = !quoted
                    started = true
                } else if (ch.isWhitespace() && !quoted) {
                    if (started) out += cur.toString()
                    cur.clear()
                    started = false
                } else {
                    cur.append(ch)
                    started = true
                }
            }
            if (started) out += cur.toString()
            return out.filter { it.isNotEmpty() }
        }
    }
}

/** Adds a filter once. */
public fun List<ColumnFilter>.adding(f: ColumnFilter): List<ColumnFilter> = if (f in this) this else this + f

/**
 * Keeps a search's column terms as filters: what the search field's enter
 * does. Returns the filters and the text left over.
 */
public fun List<ColumnFilter>.keepingTerms(query: String, columns: List<String>): Pair<List<ColumnFilter>, String> {
    val q = TableQuery.parse(query, columns)
    return q.filters.fold(this) { acc, f -> acc.adding(f) } to q.text
}

public object TableFilter {
    /**
     * The rows the kept filters and the search's terms and text pass;
     * free text matches any column.
     */
    public fun <R : ColumnFilterable> rows(rows: List<R>, columns: List<String>, query: String, kept: List<ColumnFilter>): List<R> {
        val q = TableQuery.parse(query, columns)
        val all = kept + q.filters
        val text = q.text.trim(' ', '\t')
        return rows.filter { it.passes(all) && it.holds(text) }
    }
}
