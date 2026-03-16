package crawler

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ege/google-in-a-day/internal/index"
	"github.com/ege/google-in-a-day/internal/storage"
)

// Crawler orchestrates the crawl: coordinator goroutine, worker pool, and indexer.
type Crawler struct {
	cfg     Config
	index   *index.Index
	metrics *Metrics
	db      *storage.DB // optional persistence; nil disables it
}

// Config holds crawler configuration.
type Config struct {
	SeedURL        string
	MaxDepth       int
	NumWorkers     int
	QueueSize      int
	RequestTimeout time.Duration
	MaxBodySize    int64
	SameDomain     bool
}

// Metrics holds atomic counters for observability.
type Metrics struct {
	PagesProcessed atomic.Int64
	PagesQueued    atomic.Int64
	PagesErrored   atomic.Int64
	QueueDepth     atomic.Int64
	ActiveWorkers  atomic.Int64
	IndexedDocs    atomic.Int64
	OverflowSize   atomic.Int64
	StartTime      time.Time
	CrawlDone      atomic.Bool
}

// NewCrawler creates a new crawler instance. Pass nil for db to disable persistence.
func NewCrawler(cfg Config, idx *index.Index, metrics *Metrics, db *storage.DB) *Crawler {
	return &Crawler{
		cfg:     cfg,
		index:   idx,
		metrics: metrics,
		db:      db,
	}
}

// ResumeState holds pre-loaded data for resuming an interrupted crawl.
type ResumeState struct {
	Visited map[string]bool
	Queue   []CrawlTask
}

// Start runs the crawl to completion or until the context is cancelled.
// It blocks until all crawling and indexing is done.
// Pass a non-nil resume to continue an interrupted crawl.
func (c *Crawler) Start(ctx context.Context, resume *ResumeState) {
	c.metrics.StartTime = time.Now()

	seedHost := ""
	if u, err := url.Parse(c.cfg.SeedURL); err == nil {
		seedHost = u.Host
	}

	workerCfg := WorkerConfig{
		RequestTimeout: c.cfg.RequestTimeout,
		MaxBodySize:    c.cfg.MaxBodySize,
		SameDomain:     c.cfg.SameDomain,
		SeedHost:       seedHost,
	}

	// Channels
	taskCh := make(chan CrawlTask, c.cfg.QueueSize)
	discoveredCh := make(chan []CrawlTask, c.cfg.NumWorkers*2)
	resultsCh := make(chan PageRecord, c.cfg.NumWorkers*2)

	// Set up visited set and initial queue
	visited := make(map[string]bool)
	var initialQueue []CrawlTask

	if resume != nil && len(resume.Queue) > 0 {
		// Resuming: use pre-loaded visited set and queue
		visited = resume.Visited
		initialQueue = resume.Queue
		log.Printf("Resuming crawl with %d visited URLs and %d queued tasks", len(visited), len(initialQueue))
	} else {
		// Fresh crawl: seed the first task
		seedTask := CrawlTask{
			URL:       c.cfg.SeedURL,
			OriginURL: "",
			Depth:     0,
		}
		initialQueue = []CrawlTask{seedTask}
		visited[c.cfg.SeedURL] = true
	}

	// Save crawl state to DB
	if c.db != nil {
		c.db.SaveCrawlState(storage.CrawlState{
			SeedURL:    c.cfg.SeedURL,
			MaxDepth:   c.cfg.MaxDepth,
			NumWorkers: c.cfg.NumWorkers,
			SameDomain: c.cfg.SameDomain,
		})
	}

	// Enqueue initial tasks
	enqueued := 0
	var overflow []CrawlTask
	for _, t := range initialQueue {
		select {
		case taskCh <- t:
			enqueued++
		default:
			overflow = append(overflow, t)
		}
	}
	c.metrics.PagesQueued.Add(int64(len(initialQueue)))
	c.metrics.QueueDepth.Add(int64(enqueued))
	c.metrics.ActiveWorkers.Add(int64(enqueued))

	// Launch workers
	var wg sync.WaitGroup
	for i := 0; i < c.cfg.NumWorkers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			workerLoop(ctx, id, workerCfg, taskCh, discoveredCh, resultsCh)
		}(i)
	}

	// Indexer goroutine: reads resultsCh, updates the inverted index
	indexDone := make(chan struct{})
	go func() {
		defer close(indexDone)
		var pendingPostings []storage.PostingRow
		flushInterval := time.NewTicker(3 * time.Second)
		defer flushInterval.Stop()

		for {
			select {
			case record, ok := <-resultsCh:
				if !ok {
					// Channel closed, flush remaining
					if c.db != nil && len(pendingPostings) > 0 {
						c.db.SavePostingsBatch(pendingPostings)
					}
					return
				}
				if record.Error != "" {
					c.metrics.PagesErrored.Add(1)
				} else {
					c.index.AddDocument(index.Document{
						URL:       record.URL,
						OriginURL: record.OriginURL,
						Depth:     record.Depth,
						Title:     record.Title,
						BodyText:  record.BodyText,
					})
					c.metrics.IndexedDocs.Add(1)

					// Queue postings for batch persistence
					if c.db != nil {
						pendingPostings = collectPostings(pendingPostings, record)
						if len(pendingPostings) >= 500 {
							c.db.SavePostingsBatch(pendingPostings)
							pendingPostings = pendingPostings[:0]
						}
					}
				}
				c.metrics.PagesProcessed.Add(1)
			case <-flushInterval.C:
				if c.db != nil && len(pendingPostings) > 0 {
					c.db.SavePostingsBatch(pendingPostings)
					pendingPostings = pendingPostings[:0]
				}
			}
		}
	}()

	// Coordinator goroutine: owns visited set, overflow buffer, inFlight counter
	c.coordinatorLoop(ctx, taskCh, discoveredCh, visited, overflow, enqueued)

	// Coordinator is done — close taskCh to signal workers to exit
	// (coordinatorLoop already closed taskCh)

	// Wait for workers to finish
	wg.Wait()
	// Close resultsCh so indexer can drain and exit
	close(resultsCh)
	// Wait for indexer to finish
	<-indexDone

	// Mark completion in DB
	if c.db != nil {
		if ctx.Err() != nil {
			// Interrupted — save remaining queue for resume
			log.Printf("Crawl interrupted, saving state for resume...")
		} else {
			c.db.MarkCrawlComplete()
		}
	}

	c.metrics.CrawlDone.Store(true)
	log.Printf("Crawl complete. Processed: %d, Indexed: %d, Errors: %d",
		c.metrics.PagesProcessed.Load(),
		c.metrics.IndexedDocs.Load(),
		c.metrics.PagesErrored.Load())
}

// coordinatorLoop is the sole writer to taskCh. It reads discovered URLs from
// workers, deduplicates them, and enqueues new tasks. It tracks an inFlight
// counter to detect crawl completion.
func (c *Crawler) coordinatorLoop(ctx context.Context, taskCh chan CrawlTask, discoveredCh <-chan []CrawlTask, visited map[string]bool, overflow []CrawlTask, inFlight int) {
	defer close(taskCh)

	// Batch persist visited URLs periodically
	var newVisited []string
	persistTicker := time.NewTicker(2 * time.Second)
	defer persistTicker.Stop()

	flushVisited := func() {
		if c.db != nil && len(newVisited) > 0 {
			c.db.AddVisitedURLs(newVisited)
			newVisited = newVisited[:0]
		}
	}

	saveQueue := func() {
		if c.db == nil {
			return
		}
		// Save remaining overflow + anything in taskCh buffer
		var remaining []storage.QueuedTask
		for _, t := range overflow {
			remaining = append(remaining, storage.QueuedTask{URL: t.URL, OriginURL: t.OriginURL, Depth: t.Depth})
		}
		// Drain the channel buffer
	drain:
		for {
			select {
			case t, ok := <-taskCh:
				if !ok {
					break drain
				}
				remaining = append(remaining, storage.QueuedTask{URL: t.URL, OriginURL: t.OriginURL, Depth: t.Depth})
			default:
				break drain
			}
		}
		c.db.SaveQueuedTasks(remaining)
		flushVisited()
		log.Printf("Saved %d queued tasks and %d visited URLs for resume", len(remaining), len(visited))
	}

	for inFlight > 0 || len(overflow) > 0 {
		if len(overflow) > 0 {
			select {
			case taskCh <- overflow[0]:
				overflow = overflow[1:]
				if cap(overflow) > 256 && len(overflow) < cap(overflow)/4 {
					trimmed := make([]CrawlTask, len(overflow))
					copy(trimmed, overflow)
					overflow = trimmed
				}
				inFlight++
				c.metrics.QueueDepth.Add(1)
				c.metrics.ActiveWorkers.Add(1)
				c.metrics.OverflowSize.Store(int64(len(overflow)))
			case batch := <-discoveredCh:
				inFlight--
				c.metrics.QueueDepth.Add(-1)
				c.metrics.ActiveWorkers.Add(-1)
				newTasks := c.deduplicateAndMark(batch, visited)
				for _, t := range newTasks {
					newVisited = append(newVisited, t.URL)
				}
				overflow = append(overflow, newTasks...)
				c.metrics.OverflowSize.Store(int64(len(overflow)))
			case <-persistTicker.C:
				flushVisited()
			case <-ctx.Done():
				saveQueue()
				return
			}
		} else {
			select {
			case batch := <-discoveredCh:
				inFlight--
				c.metrics.QueueDepth.Add(-1)
				c.metrics.ActiveWorkers.Add(-1)
				newTasks := c.deduplicateAndMark(batch, visited)
				for _, t := range newTasks {
					newVisited = append(newVisited, t.URL)
				}
				for _, t := range newTasks {
					select {
					case taskCh <- t:
						inFlight++
						c.metrics.QueueDepth.Add(1)
						c.metrics.ActiveWorkers.Add(1)
					default:
						overflow = append(overflow, t)
					}
				}
				c.metrics.OverflowSize.Store(int64(len(overflow)))
			case <-persistTicker.C:
				flushVisited()
			case <-ctx.Done():
				saveQueue()
				return
			}
		}
	}

	// Normal completion — clear queue and mark complete
	flushVisited()
	if c.db != nil {
		c.db.SaveQueuedTasks(nil) // clear queue
	}
}

// deduplicateAndMark filters out already-visited and over-depth URLs,
// marking new ones as visited.
func (c *Crawler) deduplicateAndMark(tasks []CrawlTask, visited map[string]bool) []CrawlTask {
	var result []CrawlTask
	for _, t := range tasks {
		if t.Depth > c.cfg.MaxDepth {
			continue
		}
		if visited[t.URL] {
			continue
		}
		visited[t.URL] = true
		result = append(result, t)
		c.metrics.PagesQueued.Add(1)
	}
	return result
}

// MetricsSnapshot returns a point-in-time snapshot of all metrics.
func (m *Metrics) Snapshot() MetricsSnapshot {
	overflow := m.OverflowSize.Load()
	return MetricsSnapshot{
		PagesProcessed:     m.PagesProcessed.Load(),
		PagesQueued:        m.PagesQueued.Load(),
		PagesErrored:       m.PagesErrored.Load(),
		QueueDepth:         m.QueueDepth.Load(),
		ActiveWorkers:      m.ActiveWorkers.Load(),
		IndexedDocs:        m.IndexedDocs.Load(),
		OverflowSize:       overflow,
		BackPressureActive: overflow > 0,
		Uptime:             time.Since(m.StartTime),
		CrawlDone:          m.CrawlDone.Load(),
	}
}

// MetricsSnapshot is a plain struct for serialization.
type MetricsSnapshot struct {
	PagesProcessed     int64         `json:"pages_processed"`
	PagesQueued        int64         `json:"pages_queued"`
	PagesErrored       int64         `json:"pages_errored"`
	QueueDepth         int64         `json:"queue_depth"`
	ActiveWorkers      int64         `json:"active_workers"`
	IndexedDocs        int64         `json:"indexed_docs"`
	OverflowSize       int64         `json:"overflow_size"`
	BackPressureActive bool          `json:"back_pressure_active"`
	Uptime             time.Duration `json:"uptime_ns"`
	UptimeStr          string        `json:"uptime"`
	CrawlDone          bool          `json:"crawl_done"`
}

func (c *Crawler) GetMetrics() *Metrics {
	return c.metrics
}

func (c *Crawler) GetIndex() *index.Index {
	return c.index
}

func FormatUptime(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

// collectPostings builds storage posting rows from a page record for batch persistence.
func collectPostings(buf []storage.PostingRow, record PageRecord) []storage.PostingRow {
	// Simple tokenizer inline — must match index.tokenize logic
	lower := strings.ToLower(record.BodyText + " " + record.Title)
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	freq := make(map[string]int)
	for _, w := range words {
		if len(w) >= 2 {
			freq[w]++
		}
	}

	titleLower := strings.ToLower(record.Title)
	urlLower := strings.ToLower(record.URL)

	for token, tf := range freq {
		buf = append(buf, storage.PostingRow{
			Token:     token,
			URL:       record.URL,
			OriginURL: record.OriginURL,
			Depth:     record.Depth,
			Title:     record.Title,
			TermFreq:  tf,
			InTitle:   strings.Contains(titleLower, token),
			InURL:     strings.Contains(urlLower, token),
		})
	}
	return buf
}
