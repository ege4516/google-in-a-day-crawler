# Product Requirements Document: Google in a Day

## 1. Problem Statement

Building a search engine from scratch—even a minimal one—forces a developer to confront every layer of the systems stack: networking, concurrency, data structures, and user-facing APIs. The goal of this project is not to compete with Google, but to prove that a single developer can build a working crawler + search engine in a short time, using only standard-library primitives and a handful of well-chosen dependencies.

The primary educational value lies in **concurrent systems design**: managing multiple workers that share mutable state (a URL queue, a visited set, an inverted index) without corrupting data, deadlocking, or exploding memory. Secondary goals include real-time observability (can I watch the system work?) and architectural clarity (can I explain every decision in a demo?).

## 2. Goals

### Functional Goals
- **Index**: `index(origin, k)` — given a seed URL, crawl to at most depth k. Never crawl the same page twice. Design for large single-machine scale with back-pressure / bounded load control.
- **Search**: `search(query)` — return relevant URLs as triples `(relevant_url, origin_url, depth)`. Search must work while indexing is active and reflect newly discovered results.
- **UI/CLI**: Provide a web dashboard to initiate indexing, initiate search, and view system state (progress, queue depth, back-pressure status).

### Non-Functional Goals
- **Concurrency safety**: All shared data structures are protected by appropriate synchronization primitives. No data races.
- **Back-pressure**: The system bounds memory usage through bounded channels and overflow buffers. It cannot explode under link-heavy pages.
- **Observability**: A live web dashboard displays metrics (pages crawled, queue depth, active workers, overflow buffer, errors) and provides a search UI.
- **Explainability**: Every architectural decision can be justified and diagrammed by a student.
- **Single binary**: The application compiles to one binary with zero runtime dependencies.

## 3. User Stories

1. **As a user**, I can start a crawl from the web dashboard by entering a seed URL, depth, and worker count, so that I do not need to use the CLI.
2. **As a user**, I can start a crawl via CLI flags (`-seed`, `-depth`, `-workers`), so that I control the scope from the terminal.
3. **As a user**, I can open the web dashboard at `localhost:8080` while the crawler is running, so that I can observe real-time progress including back-pressure status.
4. **As a user**, I can search for terms in the dashboard search box and get ranked results immediately, even before crawling is complete.
5. **As a user**, I can stop the crawler with Ctrl+C and it shuts down gracefully, finishing in-flight requests before exiting.
6. **As a user**, I can see error counts and overflow buffer depth on the dashboard so I know if pages are failing or back-pressure is active.
7. **As a user**, I can restrict crawling to the seed domain with the `-same-domain` flag to avoid crawling the entire web.

## 4. Functional Requirements

### 4.1 Crawler (`index(origin, k)`)
- Accept a seed URL via `-seed` CLI flag or web dashboard form.
- Accept max depth via `-depth` flag or dashboard form (default: 3).
- Accept worker count via `-workers` flag or dashboard form (default: 5).
- Fetch pages via HTTP GET with configurable timeout and body size limit.
- Parse HTML to extract: page title, all `<a href>` links, and visible body text.
- Normalize URLs: resolve relative paths, lowercase scheme/host, strip fragments and trailing slashes.
- Filter URLs: reject non-HTTP schemes, binary file extensions, and (optionally) off-domain links.
- Track visited URLs to prevent duplicate crawling.
- Respect depth limits: seed page = depth 0, linked pages = depth 1, etc.

### 4.2 Indexer
- Tokenize page text: lowercase, split on non-alphanumeric chars, remove stop words, remove tokens shorter than 2 characters.
- Build an inverted index mapping each token to a list of postings.
- Each posting records: document URL, origin URL, depth, title, term frequency, whether the token appears in the title, and whether it appears in the URL.

### 4.3 Search (`search(query)`)
- Accept a text query, tokenize it with the same rules as indexing.
- Score each matching document: title match (+3.0), URL match (+2.0), body term frequency (+min(tf, 5)/5.0).
- Return results as triples: `(relevant_url, origin_url, depth)` plus title and score.
- Return results sorted by score descending, capped at `topK`.
- Return results as JSON via `GET /api/search?q=<query>&k=<topK>`.

### 4.4 Dashboard / UI
- Serve a web UI at `GET /`.
- Provide a form to start a crawl (seed URL, depth, workers, same-domain toggle).
- Display live metrics: pages processed, pages queued, indexed docs, errors, queue depth, active workers, overflow buffer (back-pressure indicator), uptime.
- Provide a search box that queries the index and displays results.
- Auto-refresh every 2 seconds while crawl is running.
- Serve JSON metrics at `GET /api/metrics`.
- Serve JSON search at `GET /api/search`.
- Programmatic crawl start at `POST /api/index` (JSON body).

## 5. Non-Functional Requirements

| Requirement | Target |
|---|---|
| **Concurrency** | No data races under concurrent read/write to the index |
| **Memory** | Bounded by channel capacity + visited set + index size; no unbounded growth |
| **Throughput** | Capable of crawling hundreds of pages per minute with 5 workers |
| **Latency** | Search queries return within 50ms for indexes < 10K documents |
| **Reliability** | Graceful shutdown on SIGINT; no goroutine leaks |
| **Portability** | Single binary, runs on Linux, macOS, and Windows |
| **Data store** | In-memory inverted index serves as the local data store. SQLite persistence is a documented next step for production (see recommendation.md). |

## 6. Success Metrics (Aligned with Grading)

| Category | Weight | Criteria |
|---|---|---|
| **Functionality** | 40% | Crawler fetches pages, respects depth limit, deduplicates URLs. Search returns ranked triples. Dashboard shows live metrics and allows initiating crawls. All tests pass. |
| **Architecture** | 40% | Coordinator pattern eliminates deadlock by design. Bounded channels provide back-pressure. RWMutex on index allows concurrent search during crawling. Clear package boundaries (4 packages, ~8 files). |
| **AI Stewardship** | 20% | Clear PRD, README, and recommendation doc. Code is well-structured and explainable. Decision rationale documented. |

## 7. Out of Scope

- JavaScript-rendered SPA pages (no headless browser)
- Distributed crawling across multiple machines
- ML-based ranking (TF-IDF, BM25, PageRank)
- User authentication or multi-tenant access
- Image/PDF/video content extraction
- Full-text snippet generation with query highlighting
- Persistent sessions or user accounts

## 8. Glossary

| Term | Definition |
|---|---|
| **Inverted index** | A data structure that maps tokens (words) to the set of documents containing them. |
| **Posting** | A single entry in the inverted index, recording that a token appears in a specific document with metadata (frequency, position). |
| **Back-pressure** | A flow-control mechanism that prevents producers from overwhelming consumers. In this system: bounded channels + overflow buffer. |
| **Coordinator** | A dedicated goroutine that owns the task queue and visited set, preventing deadlock by being the sole writer to the task channel. |
| **Depth** | The number of link-hops from the seed URL. Seed = depth 0. |
| **Stop words** | Common words (the, is, and, etc.) excluded from indexing because they provide no search signal. |
| **Crawl task** | A unit of work: a URL to fetch, plus its origin URL and current depth. |
| **Overflow buffer** | A local slice in the coordinator that holds tasks when the bounded task channel is full. Its size is the back-pressure indicator. |
