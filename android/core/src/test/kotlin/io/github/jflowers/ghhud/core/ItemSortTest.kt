package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.Snapshot
import org.junit.Assert.assertEquals
import org.junit.Test
import java.io.File

/** The list's sorts, against the shared dashboard fixture. */
class ItemSortTest {
    private val s = Snapshot.parseFrom(
        File(System.getProperty("fixtures") ?: "../../testdata/clients", "dashboard.binpb").readBytes(),
    )
    private val rows = ItemList.rows(s, ItemFilter.ALL, now = s.fetchedAt.instant)

    private fun nums(sort: ItemSort, reversed: Boolean = false) = sort.sort(rows, reversed).map { it.num }

    @Test
    fun sorts() {
        assertEquals(rows.map { it.num }, nums(ItemSort.HUB, reversed = true))
        assertEquals(listOf(401, 398), nums(ItemSort.NEEDS_YOU).take(2))
        assertEquals(listOf(12, 60, 70, 398, 401, 405, 0, 0), nums(ItemSort.NUMBER))
        assertEquals(listOf(405, 401), nums(ItemSort.AGE).take(2))
        assertEquals(12, nums(ItemSort.AGE, reversed = true).first()) // no date: last, so first reversed
        assertEquals(listOf(401, 12, 405), nums(ItemSort.THREADS).take(3)) // people's threads before bots'
        assertEquals(70, nums(ItemSort.STATUS).first())
    }
}
