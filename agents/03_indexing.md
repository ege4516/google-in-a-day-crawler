# Agent: Indexing

## Role

Designs and implements the inverted index, tokenizer, and persistence strategy. Ensures the index supports concurrent read access during active crawling and can be fully restored on startup without re-crawling. Operates on the `internal/index/` and `internal/storage/` packages.

**Runs:** After Architect Agent produces an approved spec.
**Can run in parallel with:** Crawler Agent (different packages, no shared files).

---

## Inputs

- Architect Agent's data model (Posting fields, persistence strategy)
- Existing `internal/index/index.go`
- Existing `internal/storage/sqlite.go`, `pdata.go`
- Assignment constraint: index must be queryable while crawling is in progress

## Outputs

- `index.go` — `map[string][]Posting` with `sync.RWMutex`, `AddDocument`, `Lookup`, tokenizer, stop-word filter
- `pdata.go` — native flat-file persistence (format: `word url origin depth frequency`, append-only, mutex-protected)
- `sqlite.go` — `postings` table schema, `SavePostingsBatch`, `LoadAllPostings` for restore
- Indexer goroutine specification (resides in `crawler.go`): reads `resultsCh`, batches 500+ postings or flushes every 3 seconds

---

## Example Prompt

```
You are a Go engineer specializing in search index internals. Implement the inverted index
for a web crawler.

Requirements:
- The index must support concurrent reads during active writes (indexing while crawling)
- Postings must be persisted natively — no search library; implement your own storage format
- Index must be restorable on startup without re-crawling
- Tokenizer rules: lowercase, split on non-alphanumeric, remove stop words, remove tokens < 2 chars
- Result format: each posting must carry (url, origin_url, depth) for the search triple

Existing code:
[index.go]
[pdata.go]
[sqlite.go]

Review the existing implementation. For each component:
1. Confirm it satisfies the requirements or identify what is missing.
2. If anything is missing, produce a targeted implementation with a minimal diff.
3. Explain why the RWMutex placement is correct for concurrent search-during-crawl.

Output: analysis + targeted code changes if needed.
```

---

## Expected Output Format

```go
// CONFIRMED: RWMutex placement is correct.
// AddDocument holds write lock for the duration of one document update.
// Lookup holds read lock — multiple callers can Lookup concurrently.
// Indexer goroutine is the only caller of AddDocument; no write contention in practice.

func (idx *Index) AddDocument(doc Document) {
    idx.mu.Lock()
    defer idx.mu.Unlock()
    // tokenize title, body, URL
    // count term frequency per token
    // append posting to idx.postings[token]
    idx.docCount++
}

func (idx *Index) Lookup(token string) []Posting {
    idx.mu.RLock()
    defer idx.mu.RUnlock()
    return idx.postings[token]
}

// CONFIRMED: p.data format is correct and append-only.
// pdataMu sync.Mutex protects concurrent appends from multiple indexer goroutines.
// Format: "word url origin depth frequency\n"
```

---

## Decision Ownership

| Decision | Owner |
|---|---|
| Posting data model fields (TermFreq, InTitle, InURL) | Indexing Agent |
| Batch flush thresholds (500 records or 3 seconds) | Indexing Agent |
| Stop-word list (85 English words) | Indexing Agent (user can extend) |
| Minimum token length (2 characters) | Indexing Agent |
| p.data file format (space-separated fields) | Indexing Agent |
| Dual storage strategy (p.data primary, SQLite fallback) | Architect proposes → Indexing Agent implements |
| Adding new posting fields that change the file format | **Human approval required** (breaks existing p.data files) |

---

## Persistence Strategy (implemented)

```
During crawl (every 500 records or 3 seconds):
  resultsCh → indexer goroutine
    ├─ AddDocument(doc) → in-memory index (RWMutex write lock)
    ├─ AppendPostingsToFile(path, batch) → data/storage/p.data
    └─ SavePostingsBatch(batch) → SQLite postings table

On startup (RestoreIndex):
  1. Load from p.data (fast, sequential read)
  2. If p.data absent → load from SQLite postings table
  3. AddPosting() for each row → rebuild in-memory index
```

---

## Feeds Into

→ Search Agent (consumes `Index.Lookup`)
→ Testing Agent (index correctness and concurrent R/W tests)
→ Documentation Agent (persistence section of PRD)
