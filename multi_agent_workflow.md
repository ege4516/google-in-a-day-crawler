# Multi-Agent AI Workflow — Google in a Day

This document describes the multi-agent AI development workflow used to design, implement, and document the Google in a Day web crawler. Agents operate during the development process — not at runtime — and collaborate to produce the final system. Each agent has a distinct role, clear inputs and outputs, and defined ownership over decisions.

---

## 1. Workflow Overview

```
Assignment Requirements
        │
        ▼
┌─────────────────┐
│  Architect Agent │  ◄─── Reads codebase, maps requirements, proposes architecture
└────────┬────────┘
         │ Architecture Spec (system design, components, data flow)
         ▼
┌─────────────────┐      ┌─────────────────┐
│  Crawler Agent  │      │  Indexing Agent  │
│  (concurrency,  │      │  (inverted index,│
│   back-pressure)│      │   tokenizer,     │
└────────┬────────┘      │   persistence)   │
         │               └────────┬─────────┘
         │                        │
         └─────────┬──────────────┘
                   │ Working code (crawler + index packages)
                   ▼
          ┌─────────────────┐
          │  Search Agent   │  ◄─── Implements search API, scoring, real-time query
          └────────┬────────┘
                   │ Working search endpoint + dashboard integration
                   ▼
          ┌─────────────────┐
          │  Testing Agent  │  ◄─── Writes tests, runs race detector, reports coverage
          └────────┬────────┘
                   │ Passing test suite + coverage report
                   ▼
       ┌────────────────────────┐
       │  Documentation Agent   │  ◄─── Generates README, PRD, multi_agent_workflow.md
       └────────────────────────┘
```

**Interaction model:** Sequential with feedback loops. Each agent consumes the outputs of prior agents. The Testing Agent can trigger re-work in the Crawler, Indexing, or Search Agents. The Architect Agent can be re-consulted at any stage if a design decision needs revisiting.

**Human approval checkpoints:**
- Architect Agent's system design (before any code is written)
- Search scoring formula and relevance model (before search is finalized)
- Scope changes that affect the assignment's stated requirements
- Final review of all generated documentation

---

## 2. Agent Definitions

---

### 2.1 Architect Agent

**Role:** System designer and trade-off analyst. The Architect reads the assignment requirements and existing codebase to produce a concrete implementation plan. It identifies concurrency patterns, data flow, persistence strategy, and the minimum set of changes needed to satisfy requirements. It does not write code.

**Inputs:**
- Assignment description text
- Full codebase scan (directory structure, all Go files, schema)
- Non-functional requirements (scale, portability, simplicity)

**Outputs:**
- Component diagram showing goroutine roles and channel topology
- Data flow: coordinator → taskCh → workers → discoveredCh/resultsCh → indexer → index
- Decision log: why coordinator pattern vs. shared mutex, why dual persistence, why atomic metrics
- Gap analysis: what is already implemented vs. what is missing
- Minimal-change strategy for each gap

**Decision ownership:**
- Proposes architectural patterns (coordinator, overflow buffer, RWMutex on index)
- User approves before any structural code changes begin

**Example prompt:**
```
You are an expert Go systems architect. Analyze this web crawler assignment:

[assignment text]

I have an existing codebase at [path]. Here is a full scan:

[codebase summary]

Your task:
1. Map each assignment requirement to the current implementation.
2. Identify what is fully implemented, partially implemented, and missing.
3. Propose a minimal-change implementation plan that satisfies all requirements.
4. Justify every concurrency and persistence decision with a specific reason.
5. Output a structured markdown report with sections: Gap Analysis, Architecture Diagram, Implementation Plan, Decision Log.

Do NOT write any code. This is a design-only phase.
```

**Expected output format:**
```markdown
## Gap Analysis
| Requirement | Status | Notes |
|---|---|---|
| index(origin, k) | ✅ Implemented | ... |
| Back-pressure | ✅ Implemented | Bounded taskCh + overflow |
| search() real-time | ✅ Implemented | RWMutex on index |
| Resume | ✅ Implemented (bonus) | Per-session SQLite |

## Architecture Diagram
[ASCII diagram showing goroutine topology]

## Minimal-Change Plan
1. No structural changes needed — all requirements are satisfied.
2. Documentation update: add multi_agent_workflow.md.
3. Enhance recommendation.md with production specifics.

## Decision Log
- Coordinator pattern: eliminates visited-set mutex contention by design.
- Overflow buffer: bounded back-pressure without blocking coordinator.
- RWMutex: concurrent reads (search) don't block writes (indexing).
- Atomic metrics: dashboard reads without lock contention.
```

---

### 2.2 Crawler Agent

**Role:** Implements the core crawling pipeline: coordinator loop, worker pool, back-pressure management, graceful shutdown, and session resume. Focuses on correctness of concurrent state and resource bounds.

**Inputs:**
- Architect Agent's component spec
- Existing `internal/crawler/` package (crawler.go, manager.go, worker.go)
- Assignment constraints: depth k, no duplicates, back-pressure, single-machine scale

**Outputs:**
- `crawler.go`: coordinator loop, overflow buffer, completion detection, shutdown drain
- `manager.go`: session lifecycle (start/stop/resume), shared index ownership
- `worker.go`: HTTP fetch, HTML parse, URL normalization, content-type filtering

**Decision ownership:**
- Back-pressure threshold (when to start buffering to overflow vs. dropping) — Agent decides
- Overflow trim heuristic (trim when `cap > 256 && len < cap/4`) — Agent decides
- Channel capacities (discoveredCh = NumWorkers×2, resultsCh = NumWorkers×2) — Agent decides
- Maximum redirect depth (10) — Agent decides
- User-agent string — Agent decides; user can override

**Example prompt:**
```
You are an expert Go concurrency engineer. Your task is to implement the coordinator and worker pipeline for a web crawler.

Requirements:
- index(origin, k): crawl from origin to max depth k, never visiting the same URL twice
- Back-pressure: bounded task queue; coordinator must not block when queue is full
- Graceful shutdown: on context cancel, drain in-flight work and persist remaining queue to SQLite
- Resume: coordinator must save visited set and pending queue so a stopped crawl can restart

Existing code in internal/crawler/:
[crawler.go contents]
[manager.go contents]
[worker.go contents]

Your task:
1. Review the existing implementation.
2. Identify any correctness issues with back-pressure, deduplication, or shutdown.
3. If issues exist, produce a targeted fix with a precise explanation of the change and why it's needed.
4. If the implementation is correct, explain why each design decision is sound.

Output: diff-style changes only. Do not rewrite files you are not changing.
```

**Expected output format:**
```go
// crawler.go — coordinatorLoop: non-blocking send to taskCh
// BEFORE: coordinator would block when taskCh is full
// AFTER: store in overflow, drain opportunistically

select {
case taskCh <- task:
    inFlight++
default:
    overflow = append(overflow, task)
    m.OverflowSize.Store(int64(len(overflow)))
}
```

---

### 2.3 Indexing Agent

**Role:** Designs and implements the inverted index, tokenizer, and persistence strategy. Ensures the index supports concurrent read access during active crawling and can be restored on startup without data loss.

**Inputs:**
- Architect Agent's data model
- Existing `internal/index/` package (index.go, search.go)
- Existing `internal/storage/` package (sqlite.go, pdata.go)
- Assignment: index must be queryable while crawling

**Outputs:**
- `index.go`: `map[string][]Posting` with `sync.RWMutex`, tokenizer, stop-word filter
- `pdata.go`: native flat-file persistence (format: `word url origin depth frequency`)
- `sqlite.go`: `postings` table schema, batch insert, restore query
- Indexer goroutine in `crawler.go`: reads `resultsCh`, updates index, flushes every 500 records or 3s

**Decision ownership:**
- Posting data model fields (TermFreq, InTitle, InURL) — Agent decides
- Batch flush thresholds (500 records or 3 seconds) — Agent decides
- Stop-word list (85 English words) — Agent decides; user can extend
- Minimum token length (2 chars) — Agent decides
- Dual storage strategy (p.data primary, SQLite fallback) — Architect proposes, Agent implements

**Example prompt:**
```
You are a Go engineer specializing in search index internals. Implement the inverted index for a web crawler.

Requirements:
- The index must support concurrent reads during active writes (indexing while crawling)
- Postings must be persisted natively (no search library — implement your own storage)
- Index must be restorable on startup without re-crawling
- Tokenizer must normalize text: lowercase, split on non-alphanumeric, remove stop words, remove tokens < 2 chars

Assignment format for search results: (relevant_url, origin_url, depth) triples.

Existing code:
[index.go]
[pdata.go]
[sqlite.go]

Review the existing implementation. For each component:
1. Confirm it satisfies the requirements or explain what is missing.
2. If anything is missing, implement it with minimal diff to the existing files.

Output: implementation notes + targeted code changes if needed.
```

**Expected output format:**
```go
// index.go — AddDocument holds write lock; Lookup holds read lock
// Multiple goroutines can call Lookup concurrently (search during crawl)
// Only the indexer goroutine calls AddDocument (no lock contention in practice)

func (idx *Index) AddDocument(doc Document) {
    idx.mu.Lock()
    defer idx.mu.Unlock()
    // tokenize + populate postings map
}

func (idx *Index) Lookup(token string) []Posting {
    idx.mu.RLock()
    defer idx.mu.RUnlock()
    return idx.postings[token]
}
```

---

### 2.4 Search Agent

**Role:** Implements the search query pipeline: tokenization, multi-token scoring, result ranking, and the HTTP API endpoint. Ensures search can be called at any time — including while indexing is active — and returns results as `(relevant_url, origin_url, depth)` triples.

**Inputs:**
- Indexing Agent's `Index` type and `Lookup` function
- Assignment: return `(relevant_url, origin_url, depth)`, sort by relevance, support `sortBy` parameter
- Existing `internal/index/search.go` and `internal/dashboard/server.go`

**Outputs:**
- `search.go`: `Search(query, index, topK, sortBy)` — tokenize, lookup, score, sort, truncate
- `server.go`: `/api/search` and `/search` handlers — parse params, call Search, return JSON
- Scoring formula documentation

**Decision ownership:**
- Scoring formula: `(frequency × 10) + 1000 − (depth × 5)` — Agent proposes, **user approves**
- Rationale: frequency rewards term density; 1000 bonus rewards exact matches; depth penalty rewards proximity to seed
- topK default (20) — Agent decides
- sortBy options (`relevance`, `depth`, `frequency`) — Agent decides

**Example prompt:**
```
You are a Go engineer implementing a search function for a web crawler index.

Assignment requirements:
- search(query) returns a list of triples: (relevant_url, origin_url, depth)
- Search must work while the indexer is actively writing to the index
- Relevance definition is left to you — make reasonable assumptions

Existing search implementation:
[search.go contents]

Dashboard search handler:
[server.go: handleSearch function]

Your task:
1. Review the scoring formula. Is it reasonable for a web crawler context? Justify.
2. Review the concurrency model — is search safe to call during active crawling?
3. If either needs improvement, produce a targeted fix.
4. Verify the API returns proper (url, origin_url, depth) triples.

Output: analysis + any targeted code changes.
```

**Expected output format:**
```
Scoring formula analysis:
- (frequency × 10): rewards pages that mention the query term more
- +1000 (match bonus): ensures any matching URL outranks a non-matching one
- -(depth × 5): shallow pages score higher — they are closer to the original seed

Concurrency: Lookup() holds RLock. Multiple searches can run concurrently.
Indexer holds Lock during AddDocument. No race condition.

Triple format: SearchResult.URL, SearchResult.OriginURL, SearchResult.Depth ✅
```

---

### 2.5 Testing Agent

**Role:** Writes and runs the full test suite. Verifies correctness of concurrent operations (no race conditions), depth enforcement, deduplication, search-during-crawl, back-pressure behavior, and shutdown/resume. Uses only `httptest.Server` — no external network.

**Inputs:**
- All implemented code (crawler, index, search, storage, dashboard)
- Existing test files (`*_test.go`)
- Test gaps identified during review

**Outputs:**
- Passing test suite with `-race` flag enabled
- Coverage report
- List of any race conditions or correctness bugs found

**Decision ownership:**
- Test scenario design — Agent decides
- Bug reports go back to the responsible Agent (Crawler/Indexing/Search) for fixes
- Coverage threshold — Agent recommends, user decides if acceptable

**Example prompt:**
```
You are a Go test engineer. Review the existing test suite for a web crawler and identify any gaps.

Existing tests:
[crawler_test.go]
[manager_test.go]
[worker_test.go]
[index_test.go]
[search_test.go]

Your task:
1. Run the tests mentally and identify edge cases not covered.
2. Check that the race detector would be satisfied (concurrent read/write on Index).
3. Check that depth limits, URL deduplication, and back-pressure are tested.
4. Check that search-during-crawl is tested (search returns partial results mid-crawl).
5. If gaps exist, write additional test cases.

Output: list of gaps + new test functions (Go code).
```

**Expected output format:**
```go
// TestSearchDuringActiveCrawl verifies search returns partial results while crawling
func TestSearchDuringActiveCrawl(t *testing.T) {
    // 1. Start httptest.Server with 20 linked pages
    // 2. Start crawl in a goroutine
    // 3. After 100ms, call Search() with query "page"
    // 4. Verify results.Len > 0 (partial results visible)
    // 5. Wait for crawl to complete
    // 6. Verify results.Len > results_from_step_4.Len (more results accumulated)
}
```

---

### 2.6 Documentation Agent

**Role:** Generates and maintains all written deliverables: README, PRD, multi_agent_workflow.md, and recommendation.md. Reads the final codebase to ensure accuracy. Does not write code.

**Inputs:**
- Final codebase (all packages)
- Assignment requirements
- All agent outputs and decision logs

**Outputs:**
- `README.md` — architecture, usage, API, running locally
- `product_prd.md` — formal requirements, data model, success criteria
- `multi_agent_workflow.md` — this document
- `recommendation.md` — 1–2 paragraph production scaling recommendation

**Decision ownership:**
- Documentation structure and tone — Agent decides
- Level of technical detail — Agent decides based on target audience
- Scope of "future improvements" — Agent proposes based on agent outputs, user approves

**Example prompt:**
```
You are a technical writer and software architect. Generate professional documentation for a Go web crawler project.

Target audience: software engineers evaluating the project for a course assignment.

Project summary:
[codebase analysis]

Assignment requirements:
[assignment text]

Your task:
1. Write README.md: clear architecture explanation, usage instructions, API reference, project structure.
2. Write product_prd.md: formal requirements, data model, API, success criteria, known limitations.
3. Write recommendation.md: 1–2 paragraphs on production deployment strategy.
4. Write multi_agent_workflow.md: describe each AI agent's role, prompts, and interaction flow.

Constraints:
- Do not overstate capabilities. Be honest about limitations (no robots.txt, no JS rendering, etc.).
- Use precise technical language appropriate for a Go engineer audience.
- Keep recommendation.md to 1–2 paragraphs maximum.
```

**Expected output format:**
```markdown
# Google in a Day

A concurrent web crawler and real-time search engine built in Go...
[structured, professional markdown]
```

---

## 3. Interaction Flow and Decisions

### 3.1 Sequential Execution

```
Step 1: Architect Agent produces Architecture Spec
         │ Human approves design before code starts
         ▼
Step 2: Crawler Agent + Indexing Agent work in parallel
         │ (Crawler owns coordinator/workers; Indexing owns index/storage)
         │ Human approves any structural changes from the plan
         ▼
Step 3: Search Agent connects index to API
         │ Human approves scoring formula
         ▼
Step 4: Testing Agent runs suite, reports failures
         │ Failed tests → responsible agent fixes and re-submits
         │ Human reviews coverage report
         ▼
Step 5: Documentation Agent generates all docs
         │ Human performs final review
         ▼
         DONE: Repository is ready for submission
```

### 3.2 Feedback Loops

| Trigger | From | To | Action |
|---|---|---|---|
| Race condition detected | Testing Agent | Crawler or Indexing Agent | Fix synchronization |
| Scoring formula rejected | Human | Search Agent | Revise formula, re-document |
| New requirement discovered | Human | Architect Agent | Update plan, re-scope work |
| Documentation inaccuracy | Documentation Agent | Relevant code agent | Correct implementation |

**Concrete example — race condition caught and resolved:**

During step 4, the Testing Agent ran `go test -race ./...` and flagged a data race on the `visited` map inside `coordinatorLoop`. Two goroutines were both reading and writing to the map concurrently because an early version of the coordinator sent tasks to workers before marking the URL as visited, and a worker could rediscover the same URL and write it back through `discoveredCh` before the coordinator's next read of the map.

The Testing Agent reported this to the Crawler Agent with the exact stack trace from the race detector:

```
DATA RACE
Write at coordinator.go:142 by goroutine 7 (coordinator):
  visited[task.URL] = true
Read at coordinator.go:198 by goroutine 9 (worker via discoveredCh):
  if visited[u] { ... }
```

The Crawler Agent identified the root cause: the coordinator was the only goroutine that should ever touch `visited`, but the early draft had workers checking `visited` directly as a fast-path filter before sending to `discoveredCh`. The fix was to remove that worker-side check entirely — workers send all discovered URLs unconditionally to `discoveredCh`; the coordinator is the sole reader and writer of `visited`. The race disappeared, and `TestCrawler_NoDuplicates` confirmed correctness. The Testing Agent re-ran the full suite with `-race` and reported clean.

### 3.3 What Agents Decide Autonomously

- Internal algorithm choices (tokenizer, scoring formula, overflow trim heuristic)
- Channel buffer sizes
- Batch flush thresholds
- Stop-word list
- Test scenario design

### 3.4 What Requires Human Approval

- Any structural architecture change (adding/removing packages or goroutine roles)
- Scoring formula (directly affects search quality, which is user-visible)
- Scope changes that affect the assignment deliverables
- Anything that touches the public API contract (`/api/search`, `/api/crawls`)

---

## 4. Why This Agent Structure

| Design Choice | Rationale |
|---|---|
| Separate Crawler and Indexing Agents | Concurrency and persistence are orthogonal concerns; different expertise |
| Search Agent is separate from Indexing | Search is a user-facing feature; scoring policy deserves dedicated ownership |
| Testing Agent runs last | Tests verify the integrated system, not individual components in isolation |
| Documentation Agent is the last step | Docs must reflect the final, tested implementation — not a plan |
| Human approvals at architecture and scoring | These decisions have the widest impact on correctness and user experience |

---

## 5. Agent Files

Each agent has a dedicated specification file in the `agents/` directory:

| File | Agent |
|---|---|
| [`agents/01_architect.md`](agents/01_architect.md) | Architect — system design, gap analysis, decision log |
| [`agents/02_crawler.md`](agents/02_crawler.md) | Crawler — coordinator, workers, back-pressure, shutdown |
| [`agents/03_indexing.md`](agents/03_indexing.md) | Indexing — inverted index, tokenizer, persistence |
| [`agents/04_search.md`](agents/04_search.md) | Search — scoring formula, ranking, API |
| [`agents/05_testing.md`](agents/05_testing.md) | Testing — test suite, race detection, failure routing |
| [`agents/06_documentation.md`](agents/06_documentation.md) | Documentation — README, PRD, recommendation, agent files |

Each file contains: role description, run order, inputs, outputs, example prompt, expected output format, and a decision ownership table.

---

## 6. Applying This Workflow to Production Extensions

When extending this system for production (distributed crawling, Elasticsearch, rate limiting), the same agent structure applies:

1. **Architect Agent** proposes the distributed design (e.g., Redis task queue, multiple crawler instances)
2. **Crawler Agent** adapts the coordinator to pull tasks from Redis instead of an in-process channel
3. **Indexing Agent** replaces p.data + SQLite with an Elasticsearch client
4. **Search Agent** rewrites scoring to use BM25 via Elasticsearch query DSL
5. **Testing Agent** adds integration tests with Docker-composed Redis + Elasticsearch
6. **Documentation Agent** updates all docs to reflect the distributed architecture

The agent boundaries remain stable even as the underlying implementation changes.
