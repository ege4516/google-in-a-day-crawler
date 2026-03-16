# Google in a Day

A concurrent web crawler and real-time search engine built in Go. Crawls from a seed URL, indexes pages as they're discovered, and serves ranked search results through a live web dashboard — all while crawling is still in progress.

## Architecture

```
Seed URL --> Coordinator --> taskCh --> Worker Pool --> HTTP Fetch --> Parse HTML
                 ^                                          |
                 |                         +----------------+----------+
                 |                         v                           v
                 +---- discoveredCh <-- New URLs              PageRecord
                                                                   |
                                                          resultsCh --> Indexer --> Index
                                                                                    |
                                                                         Search <---+
                                                                           |
                                                                     Dashboard :8080
```

**Key design decisions:**

- **Coordinator pattern**: A dedicated goroutine is the sole writer to the task channel. Workers never write back to their own input queue. This eliminates deadlocks by construction.
- **Overflow buffer**: The coordinator uses non-blocking sends; excess URLs go into a local slice drained on the next iteration. Zero data loss, zero deadlock. The buffer size is exposed on the dashboard as the back-pressure indicator.
- **RWMutex on index**: The indexer goroutine writes (write lock), search requests read (read lock). Multiple concurrent searches are allowed during crawling.
- **Atomic metrics**: All counters use `sync/atomic` for lock-free reads from the dashboard.
- **In-memory data store with SQLite persistence**: The inverted index (`map[string][]Posting` + `RWMutex`) serves as the primary data store for fast concurrent access. SQLite (via `modernc.org/sqlite`, a pure-Go driver) persists crawl state, visited URLs, the queue, and index postings to disk — enabling **resume after interruption** without restarting the crawl from scratch.

## Prerequisites

- Go 1.21 or later

## Build

```bash
go build -o crawler ./cmd/crawler
```

Or using the Makefile:

```bash
make build
```

## Run

### Mode A: CLI-initiated crawl

```bash
./crawler -seed https://go.dev -depth 2 -workers 5
```

Starts crawling immediately and opens the dashboard for monitoring and search.

### Mode B: Dashboard-only mode

```bash
./crawler
```

Opens the dashboard at [http://localhost:8080](http://localhost:8080) where you can start a crawl via the web form.

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-seed` | (none) | Seed URL; omit for dashboard-only mode |
| `-depth` | 3 | Maximum crawl depth from seed |
| `-workers` | 5 | Number of concurrent crawler workers |
| `-queue-size` | 10000 | Bounded task queue capacity |
| `-timeout` | 10s | HTTP request timeout |
| `-max-body` | 1048576 | Maximum response body size (bytes) |
| `-same-domain` | true | Only crawl links on the seed domain |
| `-port` | 8080 | Dashboard HTTP port |
| `-data` | data | Directory for SQLite persistent storage |

## Dashboard

Once running, open [http://localhost:8080](http://localhost:8080) in your browser.

The dashboard provides:
- **Start Crawl form**: Enter a seed URL, depth, worker count, and same-domain toggle. Available when no crawl is running.
- **Live metrics**: Pages processed, queued, indexed, errors, queue depth, active workers, overflow buffer (back-pressure indicator), uptime.
- **Search box**: Query the index and get ranked results at any time, including during crawling.
- **Auto-refresh**: Updates every 2 seconds while a crawl is running.

Search results are returned as triples: `(relevant_url, origin_url, depth)` along with title and score. Results are ranked by:
- Title match: +3.0 per query token
- URL match: +2.0 per query token
- Body frequency: up to +1.0 per query token

### API Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/` | Dashboard web UI |
| `GET` | `/api/metrics` | JSON metrics snapshot |
| `GET` | `/api/search?q=<query>&k=<topK>` | JSON search results |
| `POST` | `/api/index` | Start a crawl (JSON body: `{"seed":"...","depth":N,"workers":N,"same_domain":bool}`) |

## Testing

```bash
go test -v ./...
```

With race detector (requires 64-bit C compiler):

```bash
go test -race ./...
```

### Test Coverage

- **worker_test.go**: URL normalization (relative, fragments, trailing slashes), URL filtering (schemes, extensions, same-domain), HTML parsing (title, links, body text, script/style skipping, malformed HTML)
- **index_test.go**: AddDocument + Lookup correctness, term frequency, tokenizer (lowercase, stop words, splitting), concurrent read/write safety
- **search_test.go**: Empty/stop-word queries, title-match ranking, multi-token queries, topK limiting, result field correctness
- **crawler_test.go**: Full integration with httptest server — all pages reached, depth limit enforced, no duplicates, search during crawl, context cancellation, metrics consistency

## Assumptions and Limitations

- **[A1]** Targets HTTP/HTTPS pages with HTML content only. PDFs, images, JS-rendered SPAs are out of scope.
- **[A2]** The seed URL is provided via CLI flag or web dashboard form.
- **[A3]** "Depth k" means link-hops from seed. Seed = depth 0.
- **[A4]** robots.txt support is a bonus feature, not implemented in core.
- **[A5]** The in-memory inverted index serves as the local data store. SQLite persistence is a documented production next step.
- **[A6]** Ranking is heuristic-based (title/URL/frequency), not ML-based.
- **[A7]** Target scale: hundreds to low-thousands of pages on a single machine.
- **[A8]** No persistence across restarts (resume is a documented bonus feature).

## Project Structure

```
├── cmd/crawler/main.go          Entry point, CLI flags, Manager wiring, signal handling
├── internal/
│   ├── crawler/
│   │   ├── crawler.go           Coordinator goroutine, worker pool, metrics
│   │   ├── manager.go           CrawlManager (start/stop/status for dashboard integration)
│   │   └── worker.go            HTTP fetch, HTML parse, URL normalize/filter
│   ├── index/
│   │   ├── index.go             Inverted index (map + RWMutex), tokenizer
│   │   └── search.go            Query processing, scoring, ranking
│   └── dashboard/
│       └── server.go            HTTP server, crawl form, metrics API, search API, embedded HTML
├── product_prd.md               Product requirements document
├── recommendation.md            Production deployment next steps
├── go.mod
├── Makefile
└── .gitignore
```

## Documentation

- [Product PRD](product_prd.md) — Requirements, user stories, success metrics
- [Recommendation](recommendation.md) — Production deployment next steps
