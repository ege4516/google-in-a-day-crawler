# Agent: Search

## Role

Implements the search query pipeline: tokenization, multi-token scoring, result ranking, and the HTTP API endpoints. Ensures `search(query)` can be called at any time — including while indexing is active — and returns results as `(relevant_url, origin_url, depth)` triples matching the assignment contract.

**Runs:** After the Indexing Agent has defined the `Index` type and `Lookup` function.
**Human approval required:** Scoring formula (directly affects search quality, which is user-visible).

---

## Inputs

- Indexing Agent's `Index` type and `Lookup(token) []Posting` function
- Assignment requirement: return `(relevant_url, origin_url, depth)` triples; search must reflect new results as they are discovered
- Existing `internal/index/search.go`
- Existing `internal/dashboard/server.go` (search handler)

## Outputs

- `search.go` — `Search(query, index, topK, sortBy) []SearchResult`: tokenize → lookup → score → sort → truncate
- `server.go` handlers — `/api/search` and `/search` endpoints: parse params, call Search, return JSON
- Scoring formula documentation

---

## Example Prompt

```
You are a Go engineer implementing the search function for a web crawler index.

Assignment requirements:
- search(query) returns a list of triples: (relevant_url, origin_url, depth)
- Search must work while the indexer is actively writing to the index
- Relevance definition is up to you — make reasonable assumptions and document them
- Support a sortBy parameter: relevance, depth, frequency

Existing search implementation:
[search.go contents]

Dashboard search handler:
[server.go: handleSearch function]

Your task:
1. Review the scoring formula. Is it reasonable for a web crawler context? Justify each term.
2. Verify the concurrency model — is search safe to call during active crawling?
3. Verify the API returns proper (url, origin_url, depth) triples in the JSON response.
4. If anything needs improvement, produce a targeted fix.

Output: scoring formula analysis + concurrency analysis + targeted code changes if needed.
```

---

## Expected Output Format

```
Scoring formula analysis:
  score += (termFreq × 10)     — rewards pages that mention the query term more often
  score += 1000                — flat match bonus; ensures any matching URL outranks a non-match
  score -= (depth × 5)        — depth penalty; pages closer to the seed rank higher

Rationale: for a depth-first web crawl, pages closer to the seed are generally more
authoritative. Frequency rewards term density. The flat bonus ensures the formula
doesn't produce negative scores for deep pages with low frequency.

Concurrency analysis:
  Search calls Lookup(), which acquires idx.mu.RLock().
  Indexer calls AddDocument(), which acquires idx.mu.Lock().
  Go's sync.RWMutex guarantees: multiple concurrent RLock() holders are safe;
  Lock() blocks until all RLock() holders release. No data race. ✅

Triple format:
  SearchResult.URL         → relevant_url  ✅
  SearchResult.OriginURL   → origin_url    ✅
  SearchResult.Depth       → depth         ✅
  (title, score, frequency are bonus fields, not part of the required triple)
```

---

## Scoring Formula (implemented)

For each query token that matches a posting:

```
score[url] += (posting.TermFreq × 10) + 1000 − (posting.Depth × 5)
```

| Term | Effect |
|---|---|
| `TermFreq × 10` | Pages mentioning the query word more often score higher |
| `+ 1000` | Flat bonus; any matching URL outranks any non-matching URL |
| `− (Depth × 5)` | Pages closer to the seed (lower depth) score higher |

Sort modes:
- `relevance` (default) — score descending
- `depth` — depth ascending, tie-break by score descending
- `frequency` — total term frequency descending, tie-break by score descending

---

## Decision Ownership

| Decision | Owner |
|---|---|
| Scoring formula structure | Search Agent proposes |
| Scoring formula approval | **Human** |
| `topK` default (20) | Search Agent |
| `sortBy` options and behavior | Search Agent |
| JSON response field names | Search Agent (must include url, origin_url, depth) |

---

## Real-Time Search Behavior

Search reflects all documents indexed up to the moment `Search()` acquires the read lock. Because the read lock is held only for the duration of `Lookup()` calls (not the entire search), results from documents indexed after the lock is released are visible in the next search call. This satisfies the assignment requirement that "search should be able to run while indexing is still active, reflecting new results as they are discovered."

---

## Feeds Into

→ Testing Agent (search correctness, sorting, real-time results during crawl)
→ Documentation Agent (search section of README, API reference in PRD)
