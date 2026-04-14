# Product Requirements Document: Google in a Day

## 1. Product Overview

Google in a Day is a concurrent web crawler and real-time search engine built in Go. Starting from one or more seed URLs, it crawls pages up to a configurable depth k, indexes their content into an in-memory inverted index backed by dual persistence (`data/storage/p.data` flat file and SQLite), and serves ranked search results through a live web dashboard — all while crawling is still in progress.

The project demonstrates concrete concurrent systems design: goroutine coordination, channel-based back-pressure, lock-free metrics, graceful shutdown, and session resume — within a single-binary, no-CGO Go application.

---

## 2. Problem Statement

Building a search engine from scratch forces confrontation with every layer of the systems stack: networking, concurrency, data structures, persistence, and user-facing APIs. The goal is not to compete with Google, but to prove that a single developer can build a working crawler + search engine using standard-library primitives and minimal dependencies, with provably safe concurrent access to shared state.

The primary engineering challenge is **concurrent resource management**: multiple workers share a URL queue, a visited set, and an inverted index — without corrupting data, deadlocking, or exhausting memory. The secondary challenge is **back-pressure**: a large crawl must not grow unboundedly in memory or overwhelm the machine it runs on.

---

## 3. Assignment Requirement Mapping

| Requirement | Status | Implementation |
|---|---|---|
| `index(origin, k)` — crawl to depth k from origin | ✅ | Coordinator pattern; seed = depth 0; `CrawlTask.Depth` enforced in `deduplicateAndMark()` |
| Never crawl the same page twice | ✅ | Coordinator-owned `map[string]bool`; single goroutine, no mutex needed |
| Back-pressure (bounded queue / max rate) | ✅ | `taskCh` channel with configurable capacity + overflow buffer; `OverflowSize` metric exposed |
| `search(query)` → `(url, origin_url, depth)` triples | ✅ | `Search()` returns `SearchResult{URL, OriginURL, Depth, ...}` |
| Search during active indexing | ✅ | `sync.RWMutex` on index; read lock allows concurrent searches while indexer holds write lock |
| Simple UI or CLI to initiate indexing and search | ✅ | Web dashboard (3 tabs) + CLI flags |
| System state visibility (progress, queue depth, back-pressure) | ✅ | Atomic metrics: `PagesProcessed`, `QueueDepth`, `OverflowSize`, `ActiveWorkers`, `IndexedDocs` |
| Resume after interruption (bonus) | ✅ | Per-session visited URLs + queued tasks persisted to SQLite on graceful stop |

---

## 4. Goals

### Functional
- `index(origin, k)` — crawl from a seed URL to depth k, never visiting the same page twice, with bounded resource usage and back-pressure
- `search(query)` — return ranked results as `(relevant_url, origin_url, depth)` triples, usable during active crawling
- Web dashboard to initiate crawls, search, and inspect system state in real time

### Non-Functional
- No data races under concurrent index read/write (verified with `go test -race`)
- Bounded memory via channel capacity + overflow buffer
- Graceful shutdown preserving crawl state for resume
- Single binary, no CGO, runs on Linux/macOS/Windows
- SQLite persistence for sessions, visited URLs, queue, and index postings

---

## 5. User Flows

### Starting a Crawl (Dashboard)
1. User opens `http://localhost:3600`
2. Clicks the **Create Crawler** tab
3. Enters one or more seed URLs (one per line), configures depth, max URLs, workers, queue size, same-domain toggle
4. Clicks **Start Crawling**
5. Dashboard shows live metrics cards; summary strip updates every 2 seconds

### Starting a Crawl (CLI)
1. User runs `./crawler -seed https://example.com -depth 2 -workers 5`
2. Crawl starts immediately; dashboard opens at `localhost:3600` for monitoring

### Searching Indexed Content
1. User clicks the **Search** tab
2. Enters a query in the search box, selects sort order
3. Results appear ranked by score, showing: URL (linked), title, score, depth, origin URL
4. Search works at any time — before, during, or after crawling

### Monitoring Crawl State
- Summary strip: URLs Visited, Words in DB, Active Crawlers, Total Created
- **Create Crawler** tab: per-crawl metric cards (pages processed, indexed, errors, uptime) with Stop button
- **Crawler Status** tab: table of all sessions with status badges, config, stats, timestamps, Stop/Resume buttons

### Stopping and Resuming
1. User clicks **Stop** on a running crawl (or Ctrl+C in CLI mode)
2. Crawler drains in-flight requests, persists visited set and remaining queue to SQLite
3. Session status changes to `stopped`
4. User clicks **Resume** — crawl restarts exactly from where it left off

---

## 6. Functional Requirements

### 6.1 Crawler — `index(origin, k)`

| Requirement | Implementation |
|---|---|
| Accept seed URL(s) via CLI or dashboard | `-seed` flag (comma-separated) or dashboard textarea (one per line) |
| Configurable max depth | `-depth` flag / dashboard field; seed = depth 0 |
| Configurable max URLs | `-max-urls` flag; 0 = unlimited |
| Configurable worker count | `-workers` flag; default 5 |
| Bounded task queue | `-queue-size` flag; default 10,000 |
| HTTP fetch with timeout | `http.Client` with configurable timeout (default 10s) |
| Body size limit | `io.LimitReader` with configurable max (default 1 MB) |
| Content-type filtering | Only processes `text/html` and `application/xhtml` |
| HTML parsing | `golang.org/x/net/html` tokenizer; extracts title, links, visible text; skips script/style/noscript |
| URL normalization | Resolve relative paths, lowercase scheme/host, strip fragments and trailing slashes |
| URL filtering | Reject non-HTTP(S) schemes, 40+ binary extensions, optionally off-domain links |
| Duplicate prevention | Coordinator-owned `map[string]bool`; sole writer, no mutex needed |
| Depth enforcement | `CrawlTask.Depth` checked in `deduplicateAndMark()` |
| Custom User-Agent | `GoogleInADay-Crawler/1.0 (educational project)` |
| Redirect limit | Max 10 redirects per request |

### 6.2 Back-Pressure Model

The system uses a two-layer back-pressure mechanism:

**Layer 1 — Bounded Channel:**
- `taskCh` has configurable capacity (default 10,000)
- Coordinator uses a non-blocking send (`select { case taskCh <- task: ... default: ... }`)
- Workers read from `taskCh`; the channel size limits how many tasks are queued at once

**Layer 2 — Overflow Buffer:**
- When `taskCh` is full, the coordinator stores excess tasks in a local `overflow []CrawlTask` slice
- The coordinator drains overflow into `taskCh` opportunistically when space is available
- `OverflowSize` is exposed as an atomic metric on the dashboard
- Trim heuristic: overflow slice is trimmed when `cap > 256 && len < cap/4` to prevent memory leak

**Why this is correct back-pressure:**
- Workers cannot be starved (channel always has work when overflow exists)
- Memory is bounded in practice (overflow grows only when processing is slower than discovery)
- The dashboard back-pressure indicator (`OverflowSize > 0`) tells the user when the system is under load

### 6.3 Indexer

| Requirement | Implementation |
|---|---|
| Tokenization | Lowercase, split on non-alphanumeric, remove tokens < 2 chars, remove 85 English stop words |
| Inverted index structure | `map[string][]Posting` protected by `sync.RWMutex` |
| Posting metadata | URL, OriginURL, Depth, Title, TermFreq, InTitle, InURL |
| Batch persistence | Postings written natively to `data/storage/p.data` (format: `word url origin depth frequency`) and SQLite |
| Index restoration | On startup: loads from `p.data` first; falls back to SQLite |
| Indexer goroutine | Reads `resultsCh`; batches 500+ postings or flushes every 3 seconds |

### 6.4 Search — `search(query)`

| Requirement | Implementation |
|---|---|
| Result format | `(relevant_url, origin_url, depth)` triples (+ title, score, frequency) |
| Query tokenization | Same tokenizer as indexing |
| Scoring | `(frequency × 10) + 1000 (match bonus) − (depth × 5)` per matching query token |
| Sort options | `relevance` (default, score desc), `depth` (depth asc), `frequency` (frequency desc) |
| TopK | Configurable via `k` query param; default 20 |
| Concurrent safety | Read lock on index; multiple searches run simultaneously during active crawling |
| Real-time results | Search reflects documents indexed up to the moment the read lock is acquired |

### 6.5 Dashboard

| Feature | Implementation |
|---|---|
| Three tabs | Search, Create Crawler, Crawler Status |
| Summary strip | URLs Visited, Words in DB, Active Crawlers, Total Created, Stop All, Clear History |
| Auto-refresh | JavaScript polls every 2 seconds; DOM-targeted update (no full reload) |
| Status badges | `running` (blue), `completed` (green), `stopped`/`failed` (red), `queued` (gray) |
| Dark theme | CSS-only, GitHub-inspired dark palette |
| Embedded assets | All HTML, CSS, JS embedded in Go source; no external files |

### 6.6 Session Lifecycle

| Status | Meaning |
|---|---|
| `queued` | Session created, not yet running |
| `running` | Crawl is actively processing pages |
| `completed` | All reachable pages within limits have been crawled |
| `stopped` | User-initiated stop; queue and visited set persisted for resume |
| `failed` | Crawl terminated due to error |

Live metrics (visited, indexed, error, queue counts) are written to SQLite every 2 seconds via a background session updater goroutine.

---

## 7. Non-Functional Requirements

| Requirement | How It's Met |
|---|---|
| **Concurrency safety** | `sync.RWMutex` on index; `sync/atomic` for metrics; single-goroutine coordinator owns visited set |
| **Back-pressure** | Bounded `taskCh` + coordinator overflow buffer; `OverflowSize` metric exposed |
| **Memory bounds** | Channel capacity (configurable); body size limit (1 MB); overflow slice trimming |
| **Graceful shutdown** | SIGINT/SIGTERM → context cancellation → drain in-flight → persist state → close channels → WaitGroup |
| **Persistence** | SQLite via `modernc.org/sqlite` (pure Go, no CGO); WAL mode; 5 s busy timeout |
| **Resume** | Per-session visited URLs and queued tasks stored in SQLite; `ResumeCrawlByID` reloads state |
| **Portability** | Single binary; cross-compiled via Makefile; tested on Windows, works on Linux/macOS |
| **Observability** | Atomic metrics; JSON API (`/api/metrics`); live dashboard with 2-second auto-refresh |

---

## 8. Data Model

### Core Structs

```
CrawlTask       { URL, OriginURL, Depth }
PageRecord      { URL, OriginURL, Depth, Title, BodyText, Links, StatusCode, Error }
Posting         { URL, OriginURL, Depth, Title, TermFreq, InTitle, InURL }
Document        { URL, OriginURL, Depth, Title, BodyText }
SearchResult    { URL, OriginURL, Depth, Title, Snippet, Score, Frequency }
Metrics         { PagesProcessed, PagesQueued, PagesErrored, QueueDepth, ActiveWorkers,
                  IndexedDocs, OverflowSize, MaxURLs, StartTime, CrawlDone, StopReason }
CrawlSession    { ID, OriginURL, MaxDepth, MaxURLs, NumWorkers, QueueSize, SameDomain,
                  Status, VisitedCount, QueuedCount, IndexedCount, ErrorCount,
                  StopReason, ErrorMessage, StartedAt, FinishedAt, SavedQueueCount }
```

### SQLite Schema (7 tables)

| Table | Purpose |
|---|---|
| `crawl_state` | Legacy single-row crawl config |
| `visited_urls` | Global visited URL set (legacy) |
| `queue` | Persisted task queue (session-scoped via `session_id`) |
| `documents` | Crawled page records |
| `postings` | Inverted index postings; `(token, url)` composite PK; indexed on `token` |
| `crawl_sessions` | Session metadata and live metrics |
| `session_visited_urls` | Per-session visited URL set (used for resume) |

WAL journal mode. Busy timeout 5000 ms. Tables created idempotently with `IF NOT EXISTS`.

---

## 9. API Reference

### CLI Flags

| Flag | Type | Default | Description |
|---|---|---|---|
| `-seed` | string | `""` | Seed URL(s), comma-separated |
| `-depth` | int | 3 | Max crawl depth |
| `-max-urls` | int | 0 | Max URLs (0 = unlimited) |
| `-workers` | int | 5 | Concurrent workers |
| `-queue-size` | int | 10000 | Task channel capacity |
| `-timeout` | duration | 10s | HTTP request timeout |
| `-max-body` | int | 1048576 | Max response body (bytes) |
| `-same-domain` | bool | true | Restrict to seed domain(s) |
| `-port` | int | 3600 | Dashboard HTTP port |
| `-data` | string | `"data"` | SQLite + p.data directory |

### HTTP Endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | Dashboard HTML (tabs: search, create, status) |
| `GET` | `/api/metrics` | JSON metrics snapshot |
| `GET` | `/search?query=...&sortBy=...` | JSON search results |
| `GET` | `/api/search?q=...&k=...` | JSON search results (alternate) |
| `GET` | `/api/crawls` | List all sessions |
| `POST` | `/api/crawls` | Create a new crawl |
| `GET` | `/api/crawls/{id}` | Get session details |
| `POST` | `/api/crawls/{id}/stop` | Stop a specific crawl |
| `POST` | `/api/crawls/{id}/resume` | Resume a stopped crawl |
| `DELETE` | `/api/crawls/completed` | Delete completed session records |
| `POST` | `/api/index` | Start a crawl (legacy) |
| `POST` | `/api/stop` | Stop all crawls (legacy) |

---

## 10. Architecture Summary

```
┌─────────────┐
│  main.go    │  CLI flags, signal handling, DB open, Manager creation,
│             │  RestoreIndex(), dashboard start, optional CLI crawl
└──────┬──────┘
       │
       ▼
┌─────────────┐     owns      ┌───────────────┐
│  Manager    │──────────────>│  Index        │  Shared across all crawls
│  (sessions) │               │  (RWMutex)    │  Accumulates docs over time
└──────┬──────┘               └───────────────┘
       │ 1:N
       ▼
┌─────────────┐
│  Crawler    │  One per active session
│  (session)  │
└──────┬──────┘
       │ spawns
       ├── Coordinator goroutine (owns visited set, taskCh, overflow buffer)
       ├── N Worker goroutines (HTTP fetch, HTML parse, URL filter)
       └── Indexer goroutine (tokenize, add to Index, batch-persist to SQLite + p.data)

Channels:
  taskCh       (coordinator → workers)     bounded, cap = QueueSize (back-pressure layer 1)
  discoveredCh (workers → coordinator)     bounded, cap = NumWorkers × 2
  resultsCh    (workers → indexer)         bounded, cap = NumWorkers × 2
  overflow     (coordinator local slice)   unbounded, back-pressure layer 2, size exposed as metric
```

**Coordinator is the only writer to `taskCh`.** Workers never write back to their own input channel. This eliminates deadlocks by construction — there is no cycle of mutual waits.

**Completion detection:** The coordinator maintains an `inFlight` counter (plain int, single goroutine). `inFlight == 0 && len(overflow) == 0` means the crawl is complete.

**Persistence flow:** Coordinator flushes visited URLs to SQLite every 2 seconds. Indexer batches postings every 500 records or 3 seconds. On shutdown, coordinator saves remaining queued tasks for resume.

---

## 11. Constraints and Assumptions

- **HTML only** — processes `text/html` and `application/xhtml+xml`. All other content types are skipped.
- **Depth model** — seed page = depth 0; each followed link increments depth by 1.
- **Single machine** — designed for hundreds to low-thousands of pages on localhost.
- **No robots.txt** — the crawler does not parse or honor robots.txt directives.
- **No per-host rate limiting** — workers fetch as fast as the network allows, bounded only by worker count and HTTP timeout.
- **Formula-based ranking** — `(frequency × 10) + 1000 − (depth × 5)` per matching token. No TF-IDF.
- **English stop words** — 85 English stop words removed. Other languages are indexed but not filtered.
- **Data directory** — SQLite and p.data stored in `./data/` by default; created automatically.
- **Language-native** — no crawler library, search library, or indexing framework is used. The inverted index, tokenizer, URL normalizer, HTTP worker pool, and back-pressure model are hand-rolled using Go's standard library. External dependencies are limited to `golang.org/x/net/html` (HTML tokenizer, not a scraping framework) and `modernc.org/sqlite` (pure-Go SQLite driver, not a search engine).

---

## 12. Out of Scope

- JavaScript-rendered SPA pages (no headless browser)
- Distributed crawling across multiple machines
- ML-based ranking or PageRank
- User authentication or multi-tenant access
- Image/PDF/video content extraction
- Query-highlighted snippet generation (struct field exists but unpopulated)
- robots.txt compliance
- Per-host rate limiting
- HTTPS certificate validation customization

---

## 13. Success Criteria

| Category | Criteria |
|---|---|
| **Functionality** | Crawler fetches pages, respects depth k, deduplicates URLs, enforces max-URL limit. `search(query)` returns ranked `(url, origin_url, depth)` triples. Dashboard provides live metrics, search, crawl creation and management. All 48 tests pass with `go test -race`. |
| **Architecture** | Coordinator pattern eliminates deadlock by design. Bounded channels + overflow buffer provide back-pressure. RWMutex allows concurrent search during crawling. Atomic metrics for lock-free dashboard reads. Clean 4-package structure. SQLite persistence with per-session resume. |
| **Scalability** | System handles large crawls gracefully: back-pressure prevents OOM; configurable workers and queue size allow tuning for available resources. |

---

## 14. Known Limitations

1. **Snippet field is always empty** — `SearchResult.Snippet` exists but is never populated.
2. **No robots.txt support** — the crawler does not check or honor robots.txt directives.
3. **No per-host rate limiting** — a fast crawl can saturate a single target server.
4. **Stop-word list is English-only** — non-English pages are indexed but common words in other languages are not filtered.
5. **No TF-IDF weighting** — scoring uses a fixed formula without inverse-document-frequency normalization.
6. **No proper HTTP router** — routing relies on `strings.TrimPrefix` patterns rather than a routing library.
7. **Legacy API endpoints** — `/api/index` and `/api/stop` coexist with the newer `/api/crawls` RESTful routes.

---

## 15. Future Improvements

See [recommendation.md](recommendation.md) for production deployment strategy. Key areas:

- **Distributed architecture** — Redis/Kafka task queue + Elasticsearch index + multiple crawler instances
- **robots.txt compliance** — parse and respect crawl directives per RFC 9309
- **Per-host rate limiting** — token bucket per domain to avoid overwhelming individual servers
- **Advanced ranking** — TF-IDF or BM25 for better relevance; query-highlighted snippet generation
- **Structured observability** — Prometheus metrics, structured JSON logging, centralized alerting
- **SSRF protection** — validate target URLs against internal network ranges before fetching
