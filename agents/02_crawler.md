# Agent: Crawler

## Role

Implements the core crawling pipeline: coordinator loop, worker pool, back-pressure management, graceful shutdown, and session resume. Focused on correctness of concurrent state and resource bounds. Operates on the `internal/crawler/` package.

**Runs:** After Architect Agent produces an approved spec.
**Can run in parallel with:** Indexing Agent (different packages, no shared files).

---

## Inputs

- Architect Agent's component spec and channel topology
- Existing `internal/crawler/crawler.go`, `manager.go`, `worker.go`
- Assignment constraints: depth k, no duplicate fetches, bounded back-pressure, single-machine scale

## Outputs

- `crawler.go` — coordinator loop, overflow buffer, completion detection, shutdown drain
- `manager.go` — session lifecycle (start/stop/resume), shared index ownership, session updater goroutine
- `worker.go` — HTTP fetch with timeout and body limit, HTML parse, URL normalization, content-type filtering

---

## Example Prompt

```
You are an expert Go concurrency engineer. Your task is to implement the coordinator and worker
pipeline for a web crawler.

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
4. If the implementation is correct, confirm it and explain why each design decision is sound.

Output: analysis notes + diff-style targeted changes only. Do not rewrite files you are not changing.
```

---

## Expected Output Format

```go
// FINDING: Non-blocking send to taskCh is correct — prevents coordinator from stalling
// when all workers are busy. Overflow drains opportunistically. Confirmed correct.

// FINDING: inFlight counter is a plain int owned solely by the coordinator goroutine.
// No synchronization needed. Completion condition: inFlight == 0 && len(overflow) == 0. Correct.

// CHANGE (if needed): Trim overflow slice to prevent unbounded backing array growth
if cap(overflow) > 256 && len(overflow) < cap(overflow)/4 {
    trimmed := make([]CrawlTask, len(overflow))
    copy(trimmed, overflow)
    overflow = trimmed
}
```

---

## Decision Ownership

| Decision | Owner |
|---|---|
| Back-pressure threshold (overflow vs. drop) | Crawler Agent |
| Overflow slice trim heuristic | Crawler Agent |
| Channel buffer sizes (discoveredCh = workers×2) | Crawler Agent |
| Maximum HTTP redirect count | Crawler Agent |
| User-Agent string | Crawler Agent (user can override via flag) |
| Structural changes to channel topology | **Human approval required** |

---

## Back-Pressure Model (implemented)

```
taskCh (cap = QueueSize)
    │
    ├─ Space available → coordinator sends task, inFlight++
    └─ Full → task goes to overflow []CrawlTask
                   │
                   └─ Coordinator drains overflow when taskCh has space
                      (next iteration of coordinator select loop)

OverflowSize metric → exposed on dashboard as back-pressure indicator
```

---

## Feeds Into

→ Testing Agent (crawler correctness tests)
→ Documentation Agent (architecture section of README)
