# Google in a Day

A concurrent web crawler and real-time search engine built in Go. Crawls from one or more seed URLs up to a configurable depth, indexes pages into an in-memory inverted index backed by native flat-file storage (`data/storage/p.data`) and SQLite, and serves ranked search results through a live web dashboard — all while crawling is still in progress.

Built as a course project exploring concurrent systems design: goroutines, channels, mutexes, graceful shutdown, and back-pressure control.

## Features

- **Concurrent crawling** with a configurable worker pool (default 5 workers)
- **Real-time search** that returns results while crawling is still running
- **Web dashboard** with three tabs: Search, Create Crawler, Crawler Status
- **Multiple concurrent crawls** — each tracked as an independent session
- **Dual persistence** — postings stored natively in `data/storage/p.data` (flat file); crawl sessions, visited URLs, and queued tasks in SQLite
- **Resume** — stopped crawls can be resumed from exactly where they left off
- **Back-pressure** — bounded task channel with overflow buffer; no unbounded memory growth
- **Duplicate prevention** — coordinator-owned visited set ensures each URL is fetched at most once
- **Same-domain filtering** — optionally restrict crawling to seed domain(s)
- **Graceful shutdown** — SIGINT/SIGTERM triggers orderly drain and state persistence
- **Two operating modes** — CLI-initiated crawl or dashboard-only (start crawls from the UI)

## Architecture

```
Seed URL(s) --> Coordinator --> taskCh --> Worker Pool --> HTTP Fetch --> Parse HTML
                    ^                                          |
                    |                         +----------------+----------+
                    |                         v                           v
                    +---- discoveredCh <-- New URLs              PageRecord
                                                                      |
                                                             resultsCh --> Indexer --> Index
                                                                                       |
                                                                            Search <---+
                                                                              |
                                                  SQLite <-- Persistence    Dashboard :3600
```

Four packages, clean separation:

| Package | Responsibility |
|---------|---------------|
| `cmd/crawler` | Entry point, CLI flags, signal handling, wiring |
| `internal/crawler` | Coordinator loop, worker pool, manager (session lifecycle), metrics |
| `internal/index` | Inverted index (map + RWMutex), tokenizer, search scoring |
| `internal/storage` | SQLite schema, CRUD for sessions/visited/queue; native `p.data` flat-file for postings |
| `internal/dashboard` | HTTP server, web UI (embedded HTML), REST API |

**Key design decisions:**

- **Coordinator pattern**: A single goroutine owns the visited set and is the sole writer to the task channel. Workers never write back to their own input. This eliminates deadlocks by construction.
- **Overflow buffer**: The coordinator uses non-blocking sends to `taskCh`. When the channel is full, excess URLs go into a local slice drained on the next iteration. The overflow size is exposed on the dashboard as the back-pressure indicator.
- **RWMutex on index**: The indexer goroutine holds a write lock; search requests hold a read lock. Multiple concurrent searches proceed without blocking each other.
- **Atomic metrics**: All counters use `sync/atomic` so the dashboard can read them without locks.
- **Dual persistence**: Postings are stored natively in `data/storage/p.data` (flat file, format: `word url origin depth frequency`). On startup, `RestoreIndex()` loads from `p.data` first, falling back to SQLite if the file doesn't exist. SQLite (`modernc.org/sqlite`, pure Go, no CGO) stores sessions, visited URLs, and the task queue.

## How It Works

### `index(origin, k)`

Starting a crawl (via CLI flag `-seed` or the dashboard form) triggers `Manager.StartCrawl`:

1. A `crawl_sessions` row is created in SQLite with status `running`.
2. A `Crawler` is created with the config, a session ID, the shared `Index`, and fresh `Metrics`.
3. Three buffered channels are created: `taskCh` (coordinator → workers), `discoveredCh` (workers → coordinator), `resultsCh` (workers → indexer).
4. Seed URLs are enqueued at depth 0.
5. N worker goroutines fetch pages, parse HTML, and send discovered URLs and page records back.
6. An indexer goroutine tokenizes page content and builds the inverted index.
7. The coordinator loop deduplicates URLs, enforces depth/URL limits, manages the overflow buffer, and detects completion (in-flight count reaches zero).

### `search(query)`

`Search(query, index, topK, sortBy)` is callable at any time, including during active crawling:

1. The query is tokenized with the same rules as indexing (lowercase, split on non-alphanumeric, remove stop words and short tokens).
2. For each query token, postings are looked up in the inverted index.
3. Scores accumulate per URL: **(frequency × 10) + 1000 (exact match bonus) − (depth × 5)** per matching token.
4. Results are sorted by score descending, truncated to `topK`.
5. Each result includes: `url`, `origin_url`, `depth`, `title`, `score`, `frequency`.

## Dashboard

Open [http://localhost:3600](http://localhost:3600) after starting the application.

**Three tabs:**

| Tab | What it does |
|-----|-------------|
| **Search** | Enter a query, select sort order (Relevance/Depth/Frequency), get ranked results with URL, title, score, frequency, depth, and origin |
| **Create Crawler** | Enter seed URL(s) (one per line), max depth, max URLs, workers, queue size, same-domain toggle; click Start |
| **Crawler Status** | Table of all sessions with status badges, config, live stats, and Stop/Resume buttons |

**Summary strip** at the top shows: URLs Visited, Words in DB, Active Crawlers, Total Created, Stop All, and Clear History buttons.

The dashboard auto-refreshes every 2 seconds while any crawl is active.

### Session Lifecycle

Each crawl is tracked as a session with status: `queued` → `running` → `completed` | `stopped` | `failed`.

- **Stop**: Cancel a running crawl from the dashboard or Ctrl+C. In-flight tasks are drained and the queue + visited set are persisted to SQLite.
- **Resume**: Click Resume on a stopped session. The crawl restarts from where it left off, using the saved visited set and queue.

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-seed` | _(none)_ | Seed URL(s), comma-separated. Omit for dashboard-only mode |
| `-depth` | 3 | Maximum crawl depth from seed (seed = depth 0) |
| `-max-urls` | 0 | Maximum total URLs to visit (0 = unlimited) |
| `-workers` | 5 | Number of concurrent crawler workers |
| `-queue-size` | 10000 | Bounded task channel capacity |
| `-timeout` | 10s | HTTP request timeout per page |
| `-max-body` | 1048576 | Maximum response body size in bytes (1 MB) |
| `-same-domain` | true | Only follow links on the seed domain(s) |
| `-port` | 3600 | Dashboard HTTP port |
| `-data` | data | Directory for persistent storage (SQLite DB + p.data) |

## API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/` | Web dashboard (HTML) |
| `GET` | `/api/metrics` | JSON metrics snapshot for the most recent crawl |
| `GET` | `/search?query=<query>&sortBy=relevance` | JSON search results (also: `/api/search?q=<query>&k=<topK>`) |
| `GET` | `/api/crawls` | List all crawl sessions (JSON) |
| `POST` | `/api/crawls` | Create a new crawl (JSON body) |
| `GET` | `/api/crawls/{id}` | Get a specific session |
| `POST` | `/api/crawls/{id}/stop` | Stop a specific crawl |
| `POST` | `/api/crawls/{id}/resume` | Resume a stopped crawl |
| `DELETE` | `/api/crawls/completed` | Delete completed session records |
| `POST` | `/api/index` | Start a crawl (legacy endpoint) |
| `POST` | `/api/stop` | Stop all crawls (legacy endpoint) |

## Running Locally

### Prerequisites

- Go 1.21 or later

### Build and run

```bash
# Build
make build          # or: go build -o crawler ./cmd/crawler

# Run with a seed URL
make run            # or ./crawler (for Linux and MacOS) 
                    # or .\crawler.exe (for Windows)        
# Run in dashboard-only mode
./crawler     #.\crawler.exe (for Windows) 

# Open the dashboard
# http://localhost:3600
```

### Run tests

```bash
make test           # or: go test ./...

# With race detector
go test -race ./...
```

## Testing

~37 tests across 5 files:

| File | Scope |
|------|-------|
| `crawler_test.go` | Integration: all pages reached, depth limit, no duplicates, search during crawl, context cancellation, metrics consistency |
| `manager_test.go` | Manager lifecycle: start, concurrent crawls, stop, stop-by-ID, running state, index accumulation across crawls |
| `worker_test.go` | Unit: URL normalization, URL filtering (schemes, extensions, same-domain), HTML parsing (title, links, body, script/style skipping) |
| `index_test.go` | Unit: add/lookup, term frequency, doc count, tokenizer rules, concurrent read/write safety |
| `search_test.go` | Unit: empty/stop-word queries, title-match ranking, multi-token queries, topK, result fields |

All integration tests use `httptest.Server` with controlled link graphs — no external network calls.

## Project Structure

```
├── cmd/crawler/main.go              Entry point, CLI flags, signal handling, wiring
├── internal/
│   ├── crawler/
│   │   ├── crawler.go               Coordinator loop, worker launch, indexer goroutine, metrics
│   │   ├── crawler_test.go          Integration tests
│   │   ├── manager.go               Session lifecycle (start/stop/resume), shared index owner
│   │   ├── manager_test.go          Manager lifecycle tests
│   │   ├── worker.go                HTTP fetch, HTML parse, URL normalize/filter
│   │   └── worker_test.go           Worker unit tests
│   ├── index/
│   │   ├── index.go                 Inverted index (map[string][]Posting + RWMutex), tokenizer
│   │   ├── index_test.go            Index unit tests
│   │   ├── search.go                Query scoring and ranking
│   │   └── search_test.go           Search unit tests
│   ├── storage/
│   │   ├── sqlite.go                SQLite schema (7 tables), all persistence CRUD
│   │   └── pdata.go                 Native p.data file writer/reader for postings
│   └── dashboard/
│       └── server.go                HTTP server, embedded HTML dashboard, REST API
├── Makefile                         build / run / test / clean targets
├── go.mod
├── product_prd.md                   Product requirements document
├── recommendation.md                Production next-steps recommendation
```

## Assumptions and Limitations

- **HTML only** — fetches HTTP/HTTPS pages with `text/html` or `application/xhtml` content types. PDFs, images, JS-rendered SPAs, and binary files are skipped.
- **Formula-based ranking** — scoring uses `(frequency × 10) + 1000 − (depth × 5)` per matching token. Supports `sortBy` parameter: `relevance`, `depth`, `frequency`.
- **No robots.txt** — the crawler does not parse or honor robots.txt directives.
- **No per-host rate limiting** — workers fetch as fast as the network allows, bounded only by worker count and timeouts.
- **Snippet field unused** — the `SearchResult` struct has a `Snippet` field but it is never populated; results show title and score only.
- **Single machine** — designed for hundreds to low-thousands of pages on localhost.
- **SQLite for persistence** — suitable for this scale; a production system would need a distributed store.

## Further Reading

- [Product PRD](product_prd.md) — Detailed requirements, data model, and architecture rationale
- [Recommendation](recommendation.md) — Production deployment and scaling next steps
