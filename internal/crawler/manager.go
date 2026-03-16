package crawler

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/ege/google-in-a-day/internal/index"
	"github.com/ege/google-in-a-day/internal/storage"
)

// Manager coordinates crawl lifecycle so the dashboard and main.go can start/stop
// crawls without leaking Crawler internals. It owns the Index (stable across crawls)
// and the current Metrics pointer.
type Manager struct {
	mu              sync.Mutex
	idx             *index.Index
	metrics         *Metrics // current or most recent crawl; never nil
	running         bool
	cancelFn        context.CancelFunc
	done            chan struct{}
	parentCtx       context.Context
	db              *storage.DB // optional persistence; nil disables it
	activeSessionID int64       // DB id of the currently running crawl session
}

// NewManager creates a Manager with a fresh Index and zeroed Metrics.
// Pass nil for db to disable persistence.
func NewManager(ctx context.Context, db *storage.DB) *Manager {
	return &Manager{
		idx:       index.NewIndex(),
		metrics:   &Metrics{},
		parentCtx: ctx,
		db:        db,
	}
}

// HasResumableState checks if there's an incomplete crawl in the DB that can be resumed.
func (m *Manager) HasResumableState() (*storage.CrawlState, bool) {
	if m.db == nil {
		return nil, false
	}
	state, completed, err := m.db.LoadCrawlState()
	if err != nil || state == nil || completed {
		return nil, false
	}
	return state, true
}

// RestoreIndex loads all persisted postings back into the in-memory index.
func (m *Manager) RestoreIndex() error {
	if m.db == nil {
		return nil
	}
	postings, err := m.db.LoadAllPostings()
	if err != nil {
		return err
	}
	if len(postings) == 0 {
		return nil
	}

	// Count unique URLs to set doc count
	urls := make(map[string]struct{})
	for _, p := range postings {
		m.idx.AddPosting(p.Token, index.Posting{
			URL:       p.URL,
			OriginURL: p.OriginURL,
			Depth:     p.Depth,
			Title:     p.Title,
			TermFreq:  p.TermFreq,
			InTitle:   p.InTitle,
			InURL:     p.InURL,
		})
		urls[p.URL] = struct{}{}
	}
	m.idx.SetDocCount(len(urls))
	log.Printf("Restored %d postings for %d documents from database", len(postings), len(urls))
	return nil
}

// ResumeCrawl starts a crawl using persisted state from the database.
func (m *Manager) ResumeCrawl(cfg Config) (<-chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return nil, errors.New("a crawl is already in progress")
	}
	if m.db == nil {
		return nil, errors.New("no database configured for resume")
	}

	// Load visited URLs
	visited, err := m.db.LoadVisitedURLs()
	if err != nil {
		return nil, err
	}

	// Load queued tasks
	queuedRows, err := m.db.LoadQueuedTasks()
	if err != nil {
		return nil, err
	}

	var queue []CrawlTask
	for _, qt := range queuedRows {
		queue = append(queue, CrawlTask{URL: qt.URL, OriginURL: qt.OriginURL, Depth: qt.Depth})
	}

	if len(queue) == 0 {
		return nil, errors.New("no queued tasks to resume")
	}

	m.metrics = &Metrics{}
	m.running = true

	crawlCtx, cancelFn := context.WithCancel(m.parentCtx)
	m.cancelFn = cancelFn

	// Create session record
	sessionID := m.createSession(cfg, "running")
	m.activeSessionID = sessionID

	c := NewCrawler(cfg, m.idx, m.metrics, m.db)

	done := make(chan struct{})
	m.done = done
	go func() {
		defer func() {
			m.finishSession(sessionID)
			m.mu.Lock()
			m.running = false
			m.activeSessionID = 0
			m.mu.Unlock()
			close(done)
		}()
		m.startSessionUpdater(crawlCtx, sessionID)
		c.Start(crawlCtx, &ResumeState{Visited: visited, Queue: queue})
	}()

	return done, nil
}

// StartCrawl launches a crawl in a background goroutine. Returns the session ID
// and a done channel. Returns an error if a crawl is already in progress.
func (m *Manager) StartCrawl(cfg Config) (int64, <-chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return 0, nil, errors.New("a crawl is already in progress")
	}

	// Clear previous resume state for a fresh crawl
	if m.db != nil {
		m.db.ClearAll()
	}

	m.metrics = &Metrics{}
	m.running = true

	crawlCtx, cancelFn := context.WithCancel(m.parentCtx)
	m.cancelFn = cancelFn

	// Create session record
	sessionID := m.createSession(cfg, "running")
	m.activeSessionID = sessionID

	c := NewCrawler(cfg, m.idx, m.metrics, m.db)

	done := make(chan struct{})
	m.done = done
	go func() {
		defer func() {
			m.finishSession(sessionID)
			m.mu.Lock()
			m.running = false
			m.activeSessionID = 0
			m.mu.Unlock()
			close(done)
		}()
		m.startSessionUpdater(crawlCtx, sessionID)
		c.Start(crawlCtx, nil)
	}()

	return sessionID, done, nil
}

// createSession inserts a crawl session record into the DB. Returns 0 if no DB.
func (m *Manager) createSession(cfg Config, status string) int64 {
	if m.db == nil {
		return 0
	}
	id, err := m.db.CreateCrawlSession(storage.CrawlSession{
		OriginURL:  cfg.SeedURL,
		MaxDepth:   cfg.MaxDepth,
		MaxURLs:    cfg.MaxURLs,
		NumWorkers: cfg.NumWorkers,
		QueueSize:  cfg.QueueSize,
		SameDomain: cfg.SameDomain,
		Status:     status,
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		log.Printf("Warning: failed to create crawl session: %v", err)
		return 0
	}
	return id
}

// startSessionUpdater launches a goroutine that periodically writes live metrics to the DB.
func (m *Manager) startSessionUpdater(ctx context.Context, sessionID int64) {
	if m.db == nil || sessionID == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				snap := m.metrics.Snapshot()
				m.db.UpdateCrawlSessionMetrics(sessionID,
					snap.PagesProcessed, snap.PagesQueued, snap.IndexedDocs, snap.PagesErrored)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// finishSession writes final metrics and status to the DB session record.
func (m *Manager) finishSession(sessionID int64) {
	if m.db == nil || sessionID == 0 {
		return
	}
	snap := m.metrics.Snapshot()
	status := "completed"
	reason := snap.StopReason
	switch reason {
	case "stopped":
		status = "stopped"
	case "url_limit":
		status = "completed"
	case "failed":
		status = "failed"
	default:
		status = "completed"
	}
	m.db.FinishCrawlSession(sessionID, status, reason,
		time.Now().UTC().Format(time.RFC3339),
		snap.PagesProcessed, snap.PagesQueued, snap.IndexedDocs, snap.PagesErrored)
}

// StopCrawl cancels the running crawl, if any.
func (m *Manager) StopCrawl() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancelFn != nil {
		m.cancelFn()
	}
}

// IsRunning reports whether a crawl is currently in progress.
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// GetMetrics returns the current Metrics pointer. Never returns nil.
func (m *Manager) GetMetrics() *Metrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.metrics
}

// GetIndex returns the shared Index. Stable across crawls.
func (m *Manager) GetIndex() *index.Index {
	return m.idx
}

// Done returns a channel that closes when the current crawl finishes.
// Returns nil if no crawl has been started.
func (m *Manager) Done() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.done
}

// GetDB returns the storage database (may be nil).
func (m *Manager) GetDB() *storage.DB {
	return m.db
}

// ActiveSessionID returns the DB id of the currently running crawl (0 if none).
func (m *Manager) ActiveSessionID() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeSessionID
}
