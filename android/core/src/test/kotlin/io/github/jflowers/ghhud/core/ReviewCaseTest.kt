package io.github.jflowers.ghhud.core

import io.github.jflowers.ghhud.api.v1.case
import io.github.jflowers.ghhud.api.v1.caseFacts
import io.github.jflowers.ghhud.api.v1.decision
import io.github.jflowers.ghhud.api.v1.proposal
import io.github.jflowers.ghhud.api.v1.reviewReply
import org.junit.Test
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue

class ReviewCaseTest {
    @Test
    fun aReviewCaseShowsItsDrafts() {
        val drafting = case {
            id = "review_reply|acme/r|#12"
            kind = "review_reply"
            repo = "acme/r"
            pr = 12
            facts = caseFacts { prs += 12 }
            supervising = true
        }
        val row = CaseRow.of(drafting)
        assertEquals("acme/r#12 review threads", row.title)
        assertEquals("", row.scope)
        assertEquals("supervisor drafting replies…", row.status)
        assertTrue(row.isReview)

        val drafted = drafting.toBuilder().setSupervising(false).setProposal(
            proposal {
                id = "p2"
                status = "pending"
                decision = decision {
                    kind = "answer"
                    summary = "Answered the LRU question."
                    replies += reviewReply {
                        path = "index/cache.go"
                        line = 42
                        author = "alice"
                        quote = "Why a map\nand not an LRU?"
                        body = "Per run."
                    }
                }
            },
        ).build()
        val r = CaseRow.of(drafted)
        assertEquals("supervisor drafted 1 reply", r.status)
        assertEquals("p2", r.proposal)
        assertEquals(listOf(CaseRow.Reply("index/cache.go:42", "alice", "Why a map and not an LRU?", "Per run.")), r.replies)
    }
}
