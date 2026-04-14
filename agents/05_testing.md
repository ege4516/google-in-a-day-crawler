# Agent: Testing

## Role

Writes and executes the full test suite. Verifies correctness of concurrent operations (no data races), depth enforcement, URL deduplication, back-pressure behavior, search-during-crawl, graceful shutdown, and session resume. Uses only `httptest.Server` — no external network calls. Reports failures back to the responsible agent for targeted fixes.

**Runs:** After all implementation agents (Crawler, Indexing, Search) have completed their work.
**Triggers re-work:** Yes — any test failure or race condition goes back to the responsible agent.

---

## Inputs

- All implemented code: `internal/crawler/`, `internal/index/`, `internal/storage/`, `internal/dashboard/`
- Existing test files (`*_test.go` across all packages)
- Assignment scenarios to verify

## Outputs

- Full passing test suite with `go test -race ./...`
- Coverage report (`go test -cover ./...`)
- List of any race conditions or correctness bugs found, with responsible agent identified

---

## Example Prompt

```
You are a Go test engineer. Review the existing test suite for a web crawler and
identify any coverage gaps or correctness issues.

Existing tests:
[crawler_test.go]
[manager_test.go]
[worker_test.go]
[index_test.go]
[search_test.go]

Assignment scenarios to verify:
1. Crawl visits every reachable page exactly once (no duplicates)
2. Depth limit k is enforced — pages at depth k+1 are not fetched
3. Back-pressure: when taskCh is full, overflow buffer is used (not dropped, not blocking)
4. Search returns partial results while crawling is active (real-time)
5. Context cancellation triggers graceful drain and state persistence
6. Resume: a stopped crawl continues from saved state without re-visiting URLs
7. Concurrent index reads and writes have no data races (race detector)
8. Metrics are consistent (visited + errored = processed)

Your task:
1. Map each scenario to an existing test function. Mark covered / not covered.
2. For any gaps, write a new test function using httptest.Server (no external network).
3. Run the suite mentally — identify any test that would fail with the race detector.

Output: coverage map + new test functions (complete, runnable Go code).
```

---

## Expected Output Format

```
Coverage Map:
  ✅ Scenario 1 (no duplicates)       → TestCrawlerNoDuplicates
  ✅ Scenario 2 (depth limit)         → TestCrawlerDepthLimit
  ✅ Scenario 3 (back-pressure)       → TestCrawlerBackPressure (overflow metric > 0)
  ✅ Scenario 4 (real-time search)    → TestSearchDuringActiveCrawl
  ✅ Scenario 5 (context cancel)      → TestCrawlerContextCancel
  ✅ Scenario 6 (resume)              → TestManagerResumeCrawl
  ✅ Scenario 7 (race detector)       → index_test.go TestIndexConcurrentReadWrite
  ✅ Scenario 8 (metrics consistency) → TestCrawlerMetricsConsistency

All scenarios covered. No new tests needed.
```

```go
// Example new test (if gap found):
func TestSearchDuringActiveCrawl(t *testing.T) {
    // 1. Build httptest.Server with 20 linked pages
    ts := buildLinkGraph(t, 20)
    defer ts.Close()

    idx := index.New()
    mgr := crawler.NewManager(context.Background(), idx, nil, "")

    // 2. Start crawl in background
    _, done, _ := mgr.StartCrawl(crawler.Config{
        SeedURL: ts.URL, MaxDepth: 3, NumWorkers: 2, QueueSize: 100,
    })

    // 3. Search after short delay (crawl still in progress)
    time.Sleep(50 * time.Millisecond)
    results := index.Search("page", idx, 20, "relevance")

    // 4. Partial results visible mid-crawl
    if len(results) == 0 {
        t.Error("expected partial search results during crawl, got none")
    }

    // 5. Wait for completion — more results now
    <-done
    finalResults := index.Search("page", idx, 20, "relevance")
    if len(finalResults) <= len(results) {
        t.Errorf("expected more results after full crawl: before=%d after=%d",
            len(results), len(finalResults))
    }
}
```

---

## Test File Responsibility Map

| File | Package | Scope |
|---|---|---|
| `crawler_test.go` | `internal/crawler` | Integration: all pages reached, depth limit, deduplication, search during crawl, context cancel, metrics |
| `manager_test.go` | `internal/crawler` | Session lifecycle: start, stop, stop-by-ID, concurrent crawls, index accumulation, resume |
| `worker_test.go` | `internal/crawler` | Unit: URL normalization, extension filtering, same-domain filtering, HTML parse (title/links/body/script-skip) |
| `index_test.go` | `internal/index` | Unit: add/lookup, term frequency counting, doc count, tokenizer rules, concurrent R/W |
| `search_test.go` | `internal/index` | Unit: empty query, stop-word-only query, title-match ranking, multi-token, topK, sortBy modes |

---

## Decision Ownership

| Decision | Owner |
|---|---|
| Test scenario design | Testing Agent |
| Use of httptest.Server (no external network) | Testing Agent (non-negotiable) |
| Bug reports → fix responsibility | Testing Agent routes to correct implementation agent |
| Coverage threshold acceptance | Testing Agent recommends, **Human decides** |

---

## Failure Routing

| Failure Type | Route To |
|---|---|
| Race condition on `visited` map | Crawler Agent |
| Race condition on `Index` | Indexing Agent |
| Incorrect depth enforcement | Crawler Agent |
| Wrong search triple format | Search Agent |
| Persistence data loss on shutdown | Crawler Agent + Indexing Agent |
| Resume visiting already-seen URLs | Crawler Agent |

---

## Feeds Into

→ Documentation Agent (test coverage summary in README)
→ All implementation agents (failure reports trigger targeted fixes)
