package io.github.jflowers.ghhud.core

import org.junit.Assert.assertEquals
import org.junit.Test

/** The search field's terms, as the Mac's checks have them. */
class FiltersTest {
    @Test
    fun readsTypedTerms() {
        val typed = TableQuery.parse("label:\"good first issue\" -repo:acme/r fix it", ItemRow.COLUMNS)
        assertEquals(listOf(ColumnFilter.of("label", "good first issue"), ColumnFilter.of("repo", "acme/r", exclude = true)), typed.filters)
        assertEquals("fix it", typed.text)
        assertEquals(listOf("label:\"good first issue\"", "-repo:acme/r"), typed.filters.map { it.term })
        assertEquals(listOf(ColumnFilter.of("kind", "Review")), TableQuery.parse("KIND:Review", ItemRow.COLUMNS).filters)
        assertEquals("nope:x #12", TableQuery.parse("nope:x #12", ItemRow.COLUMNS).text)
    }

    @Test
    fun keepsTermsOnce() {
        val (kept, left) = listOf(ColumnFilter.of("kind", "issue"))
            .keepingTerms("crash kind:issue -author:pat", ItemRow.COLUMNS)
        assertEquals("crash", left)
        assertEquals(listOf(ColumnFilter.of("kind", "issue"), ColumnFilter.of("author", "pat", exclude = true)), kept)
    }

    @Test
    fun offersEachColumnsValues() {
        val row = ItemRow(
            id = "u", kind = "review", marker = null, isPR = true, ci = Tone.BAD, review = Tone.GOOD, conflict = false,
            num = 2, repo = "acme/r", title = "t", draft = false, author = "pat", threads = 0, botThreads = 0,
            lastActor = "", updated = null, labels = emptyList(), run = null, stage = null, latestRun = null,
        )
        assertEquals(listOf("kind", "status", "status", "repo", "title", "author"), row.filterChoices.map { it.first })
    }
}
