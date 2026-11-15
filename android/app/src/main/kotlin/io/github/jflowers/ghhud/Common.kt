package io.github.jflowers.ghhud

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Close
import androidx.compose.material.icons.filled.Search
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.InputChip
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.jflowers.ghhud.core.ColumnFilter
import io.github.jflowers.ghhud.core.FactList
import io.github.jflowers.ghhud.core.keepingTerms

// Pieces the lists and detail screens share.

/** A list's search, kept filters and sort: the view the model keeps. */
data class ListState<S>(
    val sort: S,
    val query: String = "",
    val kept: List<ColumnFilter> = emptyList(),
    val reversed: Boolean = false,
) {
    /** The search's column terms kept as filters: the search field's enter. */
    fun keepTerms(columns: List<String>): ListState<S> {
        val (k, text) = kept.keepingTerms(query, columns)
        return copy(kept = k, query = text)
    }

    fun adding(f: ColumnFilter): ListState<S> = copy(kept = if (f in kept) kept else kept + f)
}

/**
 * The search field and, under it, the kept filters as chips (tap one to
 * drop it). Enter keeps the search's column terms as chips.
 */
@Composable
fun ListSearch(
    query: String,
    kept: List<ColumnFilter>,
    placeholder: String,
    onQuery: (String) -> Unit,
    onKeep: () -> Unit,
    onRemove: (ColumnFilter) -> Unit,
    onClear: () -> Unit,
) {
    OutlinedTextField(
        query,
        onValueChange = onQuery,
        placeholder = { Text(placeholder) },
        leadingIcon = { Icon(Icons.Filled.Search, contentDescription = null) },
        trailingIcon = {
            if (query.isNotEmpty()) {
                IconButton(onClick = { onQuery("") }) { Icon(Icons.Filled.Close, contentDescription = "Clear the search") }
            }
        },
        singleLine = true,
        keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
        keyboardActions = KeyboardActions(onSearch = { onKeep() }),
        modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp).testTag("search"),
    )
    if (kept.isNotEmpty()) {
        LazyRow(
            horizontalArrangement = Arrangement.spacedBy(8.dp),
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.padding(horizontal = 12.dp),
        ) {
            items(kept, key = { it.term }) { f ->
                InputChip(
                    selected = false,
                    onClick = { onRemove(f) },
                    label = { Text(f.term) },
                    trailingIcon = { Icon(Icons.Filled.Close, contentDescription = "Remove ${f.term}") },
                    modifier = Modifier.testTag("kept:" + f.term),
                )
            }
            item { TextButton(onClick = onClear) { Text("Clear") } }
        }
    }
}

/** A list's sort menu: its orders, and Reverse once one is picked. */
@Composable
fun <S> SortMenu(sorts: List<S>, label: (S) -> String, current: S, reversed: Boolean, first: S, onPick: (S) -> Unit, onReverse: () -> Unit) {
    var open by remember { mutableStateOf(false) }
    Box {
        TextButton(onClick = { open = true }, modifier = Modifier.testTag("sort")) {
            Text(if (current == first) "Sort" else label(current) + if (reversed) " ↑" else "")
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false }) {
            for (s in sorts) {
                DropdownMenuItem(
                    text = { Text((if (s == current) "✓ " else "") + label(s)) },
                    onClick = {
                        open = false
                        onPick(s)
                    },
                    modifier = Modifier.testTag("sort:" + s.toString()),
                )
            }
            if (current != first) {
                HorizontalDivider()
                DropdownMenuItem(text = { Text("Reverse") }, onClick = {
                    open = false
                    onReverse()
                })
            }
        }
    }
}

/** A long press's menu: "Show only" and "Hide" for each of the row's values. */
@Composable
fun FilterMenu(open: Boolean, choices: List<Pair<String, String>>, onDismiss: () -> Unit, onFilter: (ColumnFilter) -> Unit) {
    DropdownMenu(expanded = open, onDismissRequest = onDismiss) {
        for ((column, value) in choices) {
            DropdownMenuItem(
                text = { Text("Show only $column: $value", maxLines = 1, overflow = TextOverflow.Ellipsis) },
                onClick = {
                    onDismiss()
                    onFilter(ColumnFilter.of(column, value))
                },
            )
        }
        HorizontalDivider()
        for ((column, value) in choices) {
            DropdownMenuItem(
                text = { Text("Hide $column: $value", maxLines = 1, overflow = TextOverflow.Ellipsis) },
                onClick = {
                    onDismiss()
                    onFilter(ColumnFilter.of(column, value, exclude = true))
                },
            )
        }
    }
}

/** A labelled fact on a detail screen. */
@Composable
fun FactRow(label: String, value: String, color: Color = Color.Unspecified) {
    Row(Modifier.fillMaxWidth().padding(vertical = 3.dp)) {
        Text(
            label,
            style = MaterialTheme.typography.bodyMedium,
            fontWeight = FontWeight.Medium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.width(96.dp),
        )
        Text(value, style = MaterialTheme.typography.bodyMedium, color = color, modifier = Modifier.testTag("fact:$label"))
    }
}

/** A titled list of lines (files, commits) on a detail screen. */
@Composable
fun FactLines(l: FactList) {
    val color = l.tone?.color() ?: Color.Unspecified
    Column(Modifier.padding(top = 10.dp).testTag("lines:" + l.title)) {
        Text(l.title, style = MaterialTheme.typography.titleSmall, color = color)
        l.note?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }
        for (line in l.lines) {
            Text(line, style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace, color = color)
        }
    }
}
