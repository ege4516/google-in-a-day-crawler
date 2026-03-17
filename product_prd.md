# Product Requirements Document: Google in a Day

## 1. Product Overview

Google in a Day is a concurrent web crawler and real-time search engine built in Go. It crawls web pages starting from one or more seed URLs up to a configurable depth, indexes their content into an in-memory inverted index, persists state to SQLite, and serves ranked search results through a web dashboard. Multiple independent crawls can run concurrently, each tracked as a session with full lifecycle management (start, stop, resume).

The project is a course assignment focused on concurrent systems design. It demonstrates goroutine coordination, channel-based communication, lock-free metrics, bounded-resource architectures, and graceful shutdown — all within a single-binary Go application.

## 2. Problem Statement

Building a search engine from scratch forces a developer to confront every layer of the systems stack: networking, concurrency, data structures, persistence, and user-facing APIs. The goal is not to compete with Google, but to prove that a single developer can build a working crawler + search engine using standard-library primitives and minimal dependencies, with provably safe concurrent access to shared state.

The primary educational value is **concurrent systems design**: managing multiple workers that share a URL queue, a visited set, and an inverted index — without corrupting data, deadlocking, or exhausting memory.

## 3. Goals

### Functional
- `index(origin, k)` — crawl from a seed URL to depth k, never visiting the same page twice, with bounded resource usage and back-pressure
- `search(query)` — return ranked results as `(relevant_url, origin_url, depth)` triples, usable during active crawling
- Web dashboard to initiate crawls, search, and inspect system state

### Non-Functional
- No data races under concurrent index read/write
- Bounded memory via channel capacity + overflow buffer
- Graceful shutdown preserving crawl state for resume
- Single binary, no CGO, runs on Linux/macOS/Windows
- SQLite persistence for sessions, visited URLs, queue, and index postings

## 4. User Flows

### Starting a Crawl (Dashboard)
1. User opens `http://localhost:8080`
2. Clicks the **Create Crawler** tab
3. Enters one or more seed URLs (one per line), configures depth, max URLs, workers, queue size, same-domain toggle
4. Clicks **Start Crawling**
5. Dashboard shows live metrics cards for running crawls; summary strip updates in real time

### Starting a Crawl (CLI)
1. User runs `./crawler -seed https://example.com -depth 2 -workers 5`
2. Crawl starts immediately; dashboard opens at `localhost:8080` for monitoring

### Searching Indexed Content
1. User clicks the **Search** tab (or navigates to `/?tab=search&q=golang`)
2. Enters a query in the search box
3. Results appear ranked by score, showing: URL (linked), title, score, depth, origin URL
4. Search works at any time — before, during, or after crawling

### Monitoring Crawl State
1. Summary strip shows: URLs Visited, Words in DB, Active Crawlers, Total Created
2. **Create Crawler** tab shows per-crawl metric cards (pages processed, indexed, errors, uptime) with individual Stop buttons
3. **Crawler Status** tab shows a table of all sessions with status badges, config, stats, timestamps, and Stop/Resume buttons

### Stopping and Resuming
1. User clicks **Stop** on a running crawl (or presses Ctrl+C for CLI mode)
2. The crawler drains in-flight requests and persists the visited set and remaining queue to SQLite
3. Session status changes to `stopped`
4. User clicks **Resume** on the stopped session
5. The crawl restarts from where it left off, using the saved state

## 5. Functional Requirements

### 5.1 Crawler (`index(origin, k)`)

| Requirement | Implementation |
|-------------|---------------|
| Accept seed URL(s) via CLI or dashboard | `-seed` flag (comma-separated) or dashboard textarea (one per line) |
| Configurable max depth | `-depth` flag / dashboard field; seed = depth 0 |
| Configurable max URLs | `-max-urls` flag / dashboard field; 0 = unlimited |
| Configurable worker count | `-workers` flag / dashboard field; default 5 |
| Bounded task queue | `-queue-size` flag / dashboard field; default 10000 |
| HTTP fetch with timeout | `http.Client` with configurable timeout (default 10s) |
| Body size limit | `io.LimitReader` with configurable max (default 1 MB) |
| Content-type filtering | Only processes `text/html` and `application/xhtml` |
| HTML parsing | `golang.org/x/net/html` tokenizer; extracts title, links, visible text; skips script/style/noscript |
| URL normalization | Resolve relative paths, lowercase scheme/host, strip fragments and trailing slashes |
| URL filtering | Reject non-HTTP schemes, 40+ binary file extensions, optionally off-domain links |
| Duplicate prevention | Coordinator-owned `map[string]bool`; single-goroutine access, no mutex needed |
| Depth enforcement | `CrawlTask.Depth` field; checked in `deduplicateAndMark()` |
| URL limit enforcement | Checked in both `deduplicateAndMark()` and `coordinatorLoop()` |
| Custom User-Agent | `GoogleInADay-Crawler/1.0 (educational project)` |
| Redirect limit | Max 10 redirects per request |

### 5.2 Indexer

| Requirement | Implementation |
|-------------|---------------|
| Tokenization | Lowercase, split on non-alphanumeric, remove tokens < 2 chars, remove 85 English stop words |
| Inverted index structure | `map[string][]Posting` protected by `sync.RWMutex` |
| Posting metadata | URL, OriginURL, Depth, Title, TermFreq, InTitle (bool), InURL (bool) |
| Batch persistence | Postings flushed to SQLite every 500 records or every 3 seconds |
| Index restoration | On startup, `RestoreIndex()` loads all persisted postings into memory |

### 5.3 Search (`search(query)`)

| Requirement | Implementation |
|-------------|---------------|
| Query tokenization | Same tokenizer as indexing |
| Scoring | Title match: +3.0, URL match: +2.0, Body frequency: +min(tf, 5) / 5.0 per query token |
| Result format | `{url, origin_url, depth, title, snippet, score}` — note: snippet is always empty |
| Sorting | Score descending |
| TopK | Configurable via `k` query param (default 20) |
| Concurrent safety | Read lock on index; multiple searches can run simultaneously during crawling |

### 5.4 Dashboard

| Requirement | Implementation |
|-------------|---------------|
| Embedded HTML | Single Go const string in `server.go`; no external templates |
| Three tabs | Search, Create Crawler, Crawler Status |
| Summary strip | URLs Visited, Words in DB, Active Crawlers, Total Created, Stop All, Clear History |
| Auto-refresh | JavaScript polls every 2 seconds; DOM-diffed update |
| Status badges | `running` (blue), `completed` (green), `stopped` (red), `failed` (red), `queued` (gray) |
| Dark theme | CSS-only, GitHub-inspired dark palette |

### 5.5 Session Lifecycle

| Status | Meaning |
|--------|---------|
| `queued` | Session created, not yet running |
| `running` | Crawl is actively processing pages |
| `completed` | All reachable pages within limits have been crawled |
| `stopped` | User-initiated stop; queue and visited set persisted for resume |
| `failed` | Crawl terminated due to error |

Live metrics (visited count, indexed count, error count, queue count) are updated in the DB every 2 seconds.

## 6. Non-Functional Requirements

| Requirement | How It's Met |
|-------------|-------------|
| **Concurrency safety** | `sync.RWMutex` on index, `sync/atomic` for metrics, single-goroutine ownership of visited set |
| **Back-pressure** | Bounded `taskCh` channel + coordinator overflow buffer; `OverflowSize` metric exposed |
| **Memory bounds** | Channel capacity (configurable), body size limit (1 MB), overflow slice trimming when cap > 256 and len < cap/4 |
| **Graceful shutdown** | SIGINT/SIGTERM → context cancellation → drain in-flight → persist state → close channels → wait on WaitGroup |
| **Persistence** | SQLite via `modernc.org/sqlite` (pure Go, no CGO); WAL mode, 5 s busy timeout |
| **Resume** | Per-session visited URLs and queued tasks stored in SQLite; `ResumeCrawlByID` reloads state |
| **Portability** | Single binary, cross-compiled via Makefile; tested on Windows, works on Linux/macOS |
| **Observability** | Atomic metrics, JSON API, live dashboard with auto-refresh |

## 7. Data Model

### Core Structs

```
CrawlTask       { URL, OriginURL, Depth }
PageRecord      { URL, OriginURL, Depth, Title, BodyText, Links, StatusCode, CrawledAt, Error }
Posting         { URL, OriginURL, Depth, Title, TermFreq, InTitle, InURL }
Document        { URL, OriginURL, Depth, Title, BodyText }
SearchResult    { URL, OriginURL, Depth, Title, Snippet, Score }
Metrics         { PagesProcessed, PagesQueued, PagesErrored, QueueDepth, ActiveWorkers,
                  IndexedDocs, OverflowSize, MaxURLs, StartTime, CrawlDone, StopReason }
CrawlSession    { ID, OriginURL, MaxDepth, MaxURLs, NumWorkers, QueueSize, SameDomain,
                  Status, VisitedCount, QueuedCount, IndexedCount, ErrorCount,
                  StopReason, ErrorMessage, StartedAt, FinishedAt, SavedQueueCount }
```

### SQLite Schema (7 tables)

| Table | Purpose |
|-------|---------|
| `crawl_state` | Legacy single-row crawl config |
| `visited_urls` | Global visited URL set |
| `queue` | Persisted task queue (session-scoped) |
| `documents` | Crawled page records |
| `postings` | Inverted index postings (token + URL composite key) |
| `crawl_sessions` | Session metadata and live metrics |
| `session_visited_urls` | Per-session visited URL set (for resume) |

WAL journal mode. Busy timeout 5000 ms. Tables created idempotently with `IF NOT EXISTS`.

## 8. API / Interface Summary

### CLI Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-seed` | string | `""` | Seed URL(s), comma-separated |
| `-depth` | int | 3 | Max crawl depth |
| `-max-urls` | int | 0 | Max URLs (0 = unlimited) |
| `-workers` | int | 5 | Concurrent workers |
| `-queue-size` | int | 10000 | Task channel capacity |
| `-timeout` | duration | 10s | HTTP request timeout |
| `-max-body` | int | 1048576 | Max response body (bytes) |
| `-same-domain` | bool | true | Restrict to seed domain(s) |
| `-port` | int | 8080 | Dashboard HTTP port |
| `-data` | string | `"data"` | SQLite database directory |

### HTTP Endpoints

| Method | Path | Handler | Description |
|--------|------|---------|-------------|
| GET | `/` | `handlePage` | Dashboard HTML (tabs: search, create, status) |
| GET | `/api/metrics` | `handleMetrics` | JSON metrics snapshot |
| GET | `/api/search` | `handleSearch` | JSON search results; params: `q`, `k` |
| GET | `/api/crawls` | `handleCrawls` | List all sessions |
| POST | `/api/crawls` | `handleCrawls` | Create new crawl |
| GET | `/api/crawls/{id}` | `handleCrawlByID` | Get session details |
| POST | `/api/crawls/{id}/stop` | `handleCrawlByID` | Stop a crawl |
| POST | `/api/crawls/{id}/resume` | `handleCrawlByID` | Resume a stopped crawl |
| DELETE | `/api/crawls/completed` | `handleClearCompleted` | Delete completed sessions |
| POST | `/api/index` | `handleStartCrawlJSON` | Legacy: start crawl |
| POST | `/api/stop` | `handleStopCrawlJSON` | Legacy: stop all |

## 9. Architecture Summary

```
┌─────────────┐
│  main.go    │  CLI flags, signal handling, DB open, Manager creation,
│             │  RestoreIndex(), dashboard start, optional CLI crawl
└──────┬──────┘
       │
       v
┌─────────────┐     owns      ┌─────────────┐
│  Manager    │──────────────>│  Index       │  Shared across all crawls
│  (sessions) │               │  (RWMutex)   │  Accumulates docs over time
└──────┬──────┘               └──────────────┘
       │ 1:N
       v
┌─────────────┐
│  Crawler    │  One per active session
│  (session)  │
└──────┬──────┘
       │ spawns
       ├── Coordinator goroutine (owns visited set, taskCh, overflow buffer)
       ├── N Worker goroutines (HTTP fetch, HTML parse, URL filter)
       └── Indexer goroutine (tokenize, add to Index, batch-persist to SQLite)

Channels:
  taskCh       (coordinator → workers)    bounded, cap = QueueSize
  discoveredCh (workers → coordinator)    bounded, cap = NumWorkers * 2
  resultsCh    (workers → indexer)        bounded, cap = NumWorkers * 2
```

**Coordinator is the only writer to `taskCh`**. This guarantees no deadlock: workers cannot block each other by writing to their own input channel.

**Completion detection**: The coordinator maintains an `inFlight` counter (plain int, single goroutine — no synchronization needed). `inFlight == 0 && len(overflow) == 0` means the crawl is complete.

**Persistence flow**: The coordinator flushes visited URLs to SQLite every 2 seconds. The indexer batches postings and flushes every 500 records or 3 seconds. On shutdown, the coordinator saves remaining queued tasks for resume.

## 10. Constraints and Assumptions

- **HTML only**: The crawler processes `text/html` and `application/xhtml+xml` responses. All other content types are skipped.
- **Depth model**: Seed page = depth 0. Each followed link increments depth by 1.
- **Single machine**: Designed for localhost operation at a scale of hundreds to low-thousands of pages.
- **No robots.txt**: The crawler does not parse or honor robots.txt files.
- **No rate limiting**: Workers fetch as fast as the network allows, throttled only by worker count and HTTP timeout.
- **Heuristic ranking**: Scores are based on title/URL/frequency weights. No TF-IDF, BM25, or PageRank.
- **English stop words**: The tokenizer removes 85 English stop words. Other languages are not specifically handled.
- **Data directory**: SQLite database is stored in `./data/crawler.db` by default. The directory is created if it doesn't exist.

## 11. Out of Scope

- JavaScript-rendered SPA pages (no headless browser)
- Distributed crawling across multiple machines
- ML-based ranking or PageRank
- User authentication or multi-tenant access
- Image/PDF/video content extraction
- Query-highlighted snippet generation (struct field exists but is unpopulated)
- robots.txt compliance
- Per-host rate limiting
- HTTPS certificate validation customization

## 12. Success Criteria

Aligned with course grading rubric:

| Category | Weight | Criteria |
|----------|--------|---------|
| **Functionality** | 40% | Crawler fetches pages, respects depth limit, deduplicates URLs, enforces max-URL limit. Search returns ranked triples `(url, origin_url, depth)`. Dashboard provides live metrics, search, crawl creation and management. All ~37 tests pass. |
| **Architecture** | 40% | Coordinator pattern eliminates deadlock by design. Bounded channels + overflow buffer provide back-pressure. RWMutex allows concurrent search during crawling. Atomic metrics for lock-free dashboard reads. Clean 4-package structure. SQLite persistence with per-session resume. |
| **AI Stewardship** | 20% | Clear PRD, README, and recommendation doc. Code is well-structured and explainable. Decision rationale documented in PLAN.md. |

## 13. Known Limitations

1. **Snippet field is always empty** — `SearchResult.Snippet` exists in the struct but is never populated by the search function.
2. **No robots.txt support** — the crawler does not check or honor robots.txt directives.
3. **No per-host rate limiting** — a fast crawl can saturate a single target server.
4. **Stop-word list is English-only** — non-English pages are indexed but common words in other languages are not filtered.
5. **No TF-IDF weighting** — term frequency scoring is capped at 5 occurrences without inverse-document-frequency normalization.
6. **Dashboard URL routing is path-prefix based** — no proper router library; relies on `strings.TrimPrefix` patterns.
7. **Legacy API endpoints** — `/api/index` and `/api/stop` coexist with the newer `/api/crawls` RESTful routes.

## 14. Future Improvements

See [recommendation.md](recommendation.md) for production-readiness improvements. Key areas:

- **Distributed architecture**: Replace in-memory structures with Redis/Kafka queue and Elasticsearch index
- **robots.txt compliance**: Parse and respect crawl directives
- **Per-host rate limiting**: Token-bucket or fixed-delay per domain
- **Advanced ranking**: TF-IDF or BM25 scoring, snippet generation with query highlighting
- **Structured logging and monitoring**: Prometheus metrics, centralized logging
- **SSRF protection**: Validate target URLs against internal network ranges
- **CI/CD pipeline**: Automated testing and deployment
