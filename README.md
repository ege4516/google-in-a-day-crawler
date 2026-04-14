# Google in a Day

A concurrent web crawler and real-time search engine built in Go. Crawls from one or more seed URLs up to a configurable depth k, indexes pages into an in-memory inverted index backed by dual native persistence, and serves ranked search results through a live web dashboard — while crawling is still in progress.

Built as a course project demonstrating concurrent systems design: goroutine coordination, channel-based back-pressure, lock-free metrics, graceful shutdown, and session resume.

---

## Features

- **Concurrent crawling** — configurable worker pool (default 5 workers, bounded by channel back-pressure)
- **Real-time search** — returns results while crawling is still running, with no blocking between reads and writes
- **Back-pressure** — bounded task channel + coordinator overflow buffer; `OverflowSize` metric visible on dashboard
- **Web dashboard** — three tabs: Search, Create Crawler, Crawler Status; auto-refreshes every 2 seconds
- **Multiple concurrent crawls** — each tracked as an independent session with isolated metrics
- **Dual persistence** — postings in `data/storage/p.data` (native flat file); sessions, visited URLs, and task queue in SQLite
- **Resume** — stopped crawls restart from exactly where they left off (no re-crawling visited pages)
- **Duplicate prevention** — coordinator-owned visited set; each URL fetched at most once per session
- **Same-domain filtering** — optionally restrict crawling to seed domain(s)
- **Graceful shutdown** — SIGINT/SIGTERM triggers orderly drain and state persistence
- **Two operating modes** — CLI-initiated crawl or dashboard-only (initiate from UI)

---

## Architecture

```
Seed URL(s)
    │
    ▼
Coordinator ──── taskCh (bounded) ────► Worker Pool ──── HTTP Fetch ──── Parse HTML
    ▲                                        │                                │
    │                                        │                    discoveredCh│resultsCh
    └──────────── discoveredCh ◄─────────────┘                        │       │
                 (new URLs found)                                      ▼       ▼
                                                                  Coordinator Indexer
                                                                  (dedup)      │
                                                                          ┌────▼────┐
                                                                          │  Index  │ (RWMutex)
                                                                          └────┬────┘
                                                                               │
                                                             Search ◄──────────┘
                                                               │
                                                          Dashboard :3600
                                                               │
                                                          SQLite + p.data
```

### Packages

| Package | Responsibility |
|---|---|
| `cmd/crawler` | Entry point, CLI flags, signal handling, component wiring |
| `internal/crawler` | Coordinator loop, worker pool, session manager, metrics |
| `internal/index` | Inverted index (map + RWMutex), tokenizer, search scoring |
| `internal/storage` | SQLite schema (7 tables), p.data flat-file postings |
| `internal/dashboard` | HTTP server, embedded HTML/CSS/JS dashboard, REST API |

### Key Design Decisions

**Coordinator pattern** — A single goroutine owns the visited set (`map[string]bool`) and is the sole writer to `taskCh`. Workers never write back to their own input. This eliminates deadlocks by construction: there is no cycle of mutual channel waits.

**Two-layer back-pressure** — The coordinator uses a non-blocking send to `taskCh`. When the channel is full, excess tasks go into a local `overflow []CrawlTask` slice drained on the next iteration. `OverflowSize > 0` is the back-pressure indicator on the dashboard.

**RWMutex on the index** — The indexer goroutine holds a write lock during `AddDocument`. Search calls hold a read lock during `Lookup`. Multiple concurrent searches proceed without blocking each other or stalling the indexer (write lock is held only for the duration of a single document update).

**Atomic metrics** — All counters (`PagesProcessed`, `QueueDepth`, `OverflowSize`, etc.) use `sync/atomic`. The dashboard reads them without acquiring any lock.

**Dual persistence** — Postings are appended natively to `data/storage/p.data` (format: `word url origin depth frequency`). SQLite stores sessions, visited URLs, and the queued task set. On startup, `RestoreIndex()` loads from `p.data` first (faster, sequential reads); falls back to SQLite if the file is absent.

**Language-native implementation** — No crawler library, search library, or indexing framework is used. The inverted index, tokenizer, URL normalizer, HTTP worker pool, and back-pressure model are all hand-rolled using Go's standard library (`net/http`, `net/url`, `sync`, `sync/atomic`, `context`). The only external dependencies are `golang.org/x/net/html` (the standard HTML tokenizer from the Go team, not a scraping framework) and `modernc.org/sqlite` (a pure-Go SQLite driver with no CGO — used as a plain database, not a search engine).

### Design Note: Concurrent Search During Indexing

The assignment specifically calls out search-during-indexing as a design challenge. Three mechanisms work together to make it correct:

1. **Document-level write granularity** — `AddDocument` acquires the write lock only for the duration of updating one document's postings, then releases it before the next document is processed. A search that arrives between two `AddDocument` calls sees a fully consistent, if partial, index — never a half-written document.

2. **`sync.RWMutex` — readers never block each other** — `Lookup` acquires `RLock`. Any number of concurrent search goroutines can hold `RLock` simultaneously. The indexer's `Lock` blocks only when it needs to write, and only until all current `RLock` holders release. In practice, search latency adds a sub-millisecond wait at most.

3. **Lock-free metrics via `sync/atomic`** — counters like `IndexedDocs` and `QueueDepth` are read by the dashboard without any lock. The observability layer never contends with either the indexer or the search path.

The result: a caller can invoke `Search()` at any point during an active crawl and receive results that reflect all documents indexed up to that moment, with no risk of observing a corrupted or partially-written posting list.

---

## How It Works

### `index(origin, k)`

Starting a crawl (via `-seed` flag or dashboard form) triggers `Manager.StartCrawl`:

1. A `crawl_sessions` row is created in SQLite with status `running`.
2. A `Crawler` is created with the config, a unique session ID, the shared `Index`, and fresh `Metrics`.
3. Three buffered channels are created: `taskCh` (coordinator → workers), `discoveredCh` (workers → coordinator), `resultsCh` (workers → indexer).
4. Seed URLs are enqueued at depth 0.
5. N worker goroutines fetch pages via HTTP, parse HTML, and emit discovered URLs and page records.
6. The indexer goroutine tokenizes page content and updates the inverted index; postings are batched (500 records or 3 s) to SQLite and p.data.
7. The coordinator deduplicates discovered URLs, enforces depth and URL limits, manages the overflow buffer, and detects completion when `inFlight == 0 && len(overflow) == 0`.

### `search(query)`

`Search(query, index, topK, sortBy)` is callable at any time, including during active crawling:

1. Query is tokenized with the same rules as indexing (lowercase, non-alphanumeric split, stop-word removal).
2. For each query token, postings are looked up under a read lock.
3. Scores accumulate per URL: **(frequency × 10) + 1000 (match bonus) − (depth × 5)** per matching token.
4. Results are sorted by score descending (or by `depth` or `frequency` if specified) and truncated to `topK`.
5. Each result returns: `url`, `origin_url`, `depth`, `title`, `score`, `frequency`.

---

## Dashboard

Open [http://localhost:3600](http://localhost:3600) after starting the application.

| Tab | What it does |
|---|---|
| **Search** | Enter a query, select sort order (Relevance / Depth / Frequency), view ranked results |
| **Create Crawler** | Enter seed URLs, configure depth/workers/queue; click Start; view live per-crawl metric cards |
| **Crawler Status** | Table of all sessions with status badges, config summary, live stats, Stop/Resume buttons |

**Summary strip** at the top shows: URLs Visited, Words in DB, Active Crawlers, Total Created, Stop All, Clear History.

The dashboard polls the server every 2 seconds while any crawl is active, updating only the relevant DOM nodes without a full page reload.

### Session Lifecycle

Each crawl progresses through: `queued` → `running` → `completed` | `stopped` | `failed`

- **Stop**: Cancel from dashboard or Ctrl+C. In-flight tasks are drained; queue and visited set are persisted to SQLite.
- **Resume**: Click Resume on a stopped session. Crawl restarts from the saved visited set and queue.

---

## CLI Flags

| Flag | Default | Description |
|---|---|---|
| `-seed` | _(none)_ | Seed URL(s), comma-separated. Omit for dashboard-only mode |
| `-depth` | 3 | Maximum crawl depth from seed (seed = depth 0) |
| `-max-urls` | 0 | Maximum total URLs to visit (0 = unlimited) |
| `-workers` | 5 | Number of concurrent crawler workers |
| `-queue-size` | 10000 | Bounded task channel capacity |
| `-timeout` | 10s | HTTP request timeout per page |
| `-max-body` | 1048576 | Maximum response body in bytes (1 MB) |
| `-same-domain` | true | Only follow links on the seed domain(s) |
| `-port` | 3600 | Dashboard HTTP port |
| `-data` | data | Directory for SQLite DB and p.data |

---

## API Endpoints

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/` | Web dashboard |
| `GET` | `/api/metrics` | JSON metrics for the most recent crawl |
| `GET` | `/search?query=<q>&sortBy=relevance` | JSON search results |
| `GET` | `/api/search?q=<q>&k=<topK>` | JSON search results (alternate) |
| `GET` | `/api/crawls` | List all sessions |
| `POST` | `/api/crawls` | Create a new crawl |
| `GET` | `/api/crawls/{id}` | Get a specific session |
| `POST` | `/api/crawls/{id}/stop` | Stop a crawl |
| `POST` | `/api/crawls/{id}/resume` | Resume a stopped crawl |
| `DELETE` | `/api/crawls/completed` | Delete completed session records |
| `POST` | `/api/index` | Start a crawl (legacy) |
| `POST` | `/api/stop` | Stop all crawls (legacy) |

---

## Running Locally

**Prerequisites:** Go 1.21 or later.

```bash
# Build
make build
# or: go build -o crawler ./cmd/crawler

# Run in dashboard-only mode (start crawls from the UI)
./crawler              # Linux/macOS
.\crawler.exe          # Windows

# Run with a seed URL immediately
./crawler -seed https://example.com -depth 2 -workers 5

# Open the dashboard
# http://localhost:3600
```

```bash
# Run tests
make test
# or: go test ./...

# With race detector
go test -race ./...
```

---

## Testing

48 tests across 5 files. All integration tests use `httptest.Server` with controlled link graphs — no external network calls.

| File | Scope |
|---|---|
| `crawler_test.go` | Integration: all pages reached, depth limit, deduplication, search during crawl, context cancel, metrics |
| `manager_test.go` | Session lifecycle: start, concurrent crawls, stop, stop-by-ID, index accumulation across sessions |
| `worker_test.go` | URL normalization, filtering (schemes, extensions, same-domain), HTML parsing |
| `index_test.go` | Add/lookup, term frequency, doc count, tokenizer rules, concurrent read/write safety |
| `search_test.go` | Empty/stop-word queries, title-match ranking, multi-token queries, topK, sort modes |

---

## Project Structure

```
├── cmd/crawler/main.go              Entry point, CLI flags, signal handling
├── internal/
│   ├── crawler/
│   │   ├── crawler.go               Coordinator, worker launch, indexer goroutine, metrics
│   │   ├── crawler_test.go
│   │   ├── manager.go               Session lifecycle (start/stop/resume), shared index owner
│   │   ├── manager_test.go
│   │   ├── worker.go                HTTP fetch, HTML parse, URL normalize/filter
│   │   └── worker_test.go
│   ├── index/
│   │   ├── index.go                 Inverted index (map[string][]Posting + RWMutex), tokenizer
│   │   ├── index_test.go
│   │   ├── search.go                Query scoring and ranking
│   │   └── search_test.go
│   ├── storage/
│   │   ├── sqlite.go                SQLite schema (7 tables), all persistence CRUD
│   │   └── pdata.go                 Native p.data flat-file for postings
│   └── dashboard/
│       └── server.go                HTTP server, embedded dashboard HTML, REST API
├── Makefile
├── go.mod
├── product_prd.md                   Formal product requirements document
├── recommendation.md                Production deployment recommendation
├── multi_agent_workflow.md          AI agent workflow used during development
└── agents/
    ├── 01_architect.md              Architect agent: system design, gap analysis
    ├── 02_crawler.md                Crawler agent: coordinator, workers, back-pressure
    ├── 03_indexing.md               Indexing agent: inverted index, tokenizer, persistence
    ├── 04_search.md                 Search agent: scoring formula, ranking, API
    ├── 05_testing.md                Testing agent: test suite, race detection
    └── 06_documentation.md          Documentation agent: README, PRD, recommendation
```

---

## Assumptions and Limitations

- **HTML only** — fetches `text/html` and `application/xhtml` only. PDFs, images, JS-rendered SPAs, and binary files are skipped.
- **Formula-based ranking** — `(frequency × 10) + 1000 − (depth × 5)` per matching token. No TF-IDF or BM25.
- **No robots.txt** — the crawler does not parse or honor robots.txt directives.
- **No per-host rate limiting** — workers fetch as fast as the network allows, bounded only by worker count and timeouts.
- **Snippet field unused** — `SearchResult.Snippet` exists but is never populated.
- **Single machine** — designed for hundreds to low-thousands of pages on localhost.

---

## Further Reading

- [Product PRD](product_prd.md) — formal requirements, data model, API reference, architecture rationale
- [Recommendation](recommendation.md) — production deployment and scaling strategy
- [Multi-Agent Workflow](multi_agent_workflow.md) — AI agent roles, prompts, and interaction flow used during development
