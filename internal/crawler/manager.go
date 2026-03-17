package crawler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ege/google-in-a-day/internal/index"
	"github.com/ege/google-in-a-day/internal/storage"
)

// activeCrawl tracks a single running crawl instance.
type activeCrawl struct {
	sessionID int64
	metrics   *Metrics
	cancelFn  context.CancelFunc
	done      chan struct{}
	seedURL   string
}

// RunningCrawlInfo is a read-only snapshot of an active crawl for the dashboard.
type RunningCrawlInfo struct {
	SessionID int64
	SeedURL   string
	Metrics   MetricsSnapshot
}

// Manager coordinates crawl lifecycle so the dashboard and main.go can start/stop
// crawls without leaking Crawler internals. It owns the Index (stable across crawls)
// and supports multiple concurrent crawls.
type Manager struct {
	mu        sync.Mutex
	idx       *index.Index
	crawls    map[int64]*activeCrawl // keyed by session ID
	nextID    int64                  // local ID counter when DB is nil
	parentCtx context.Context
	db        *storage.DB // optional persistence; nil disables it
}

// NewManager creates a Manager with a fresh Index.
// Pass nil for db to disable persistence.
func NewManager(ctx context.Context, db *storage.DB) *Manager {
	return &Manager{
		idx:       index.NewIndex(),
		crawls:    make(map[int64]*activeCrawl),
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

	metrics := &Metrics{}

	crawlCtx, cancelFn := context.WithCancel(m.parentCtx)

	// Create session record
	sessionID := m.createSession(cfg, "running")
	if sessionID == 0 {
		m.nextID++
		sessionID = m.nextID
	}

	ac := &activeCrawl{
		sessionID: sessionID,
		metrics:   metrics,
		cancelFn:  cancelFn,
		seedURL:   cfg.SeedURL,
	}

	c := NewCrawler(cfg, sessionID, m.idx, metrics, m.db)

	done := make(chan struct{})
	ac.done = done
	m.crawls[sessionID] = ac

	go func() {
		defer func() {
			m.finishSession(sessionID)
			m.mu.Lock()
			delete(m.crawls, sessionID)
			m.mu.Unlock()
			close(done)
		}()
		m.startSessionUpdater(crawlCtx, sessionID, metrics)
		c.Start(crawlCtx, &ResumeState{Visited: visited, Queue: queue})
	}()

	return done, nil
}

// StartCrawl launches a crawl in a background goroutine. Returns the session ID
// and a done channel. Multiple crawls can run concurrently.
func (m *Manager) StartCrawl(cfg Config) (int64, <-chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	metrics := &Metrics{}

	crawlCtx, cancelFn := context.WithCancel(m.parentCtx)

	// Create session record (falls back to in-memory ID when DB is nil)
	sessionID := m.createSession(cfg, "running")
	if sessionID == 0 {
		m.nextID++
		sessionID = m.nextID
	}

	ac := &activeCrawl{
		sessionID: sessionID,
		metrics:   metrics,
		cancelFn:  cancelFn,
		seedURL:   cfg.SeedURL,
	}

	c := NewCrawler(cfg, sessionID, m.idx, metrics, m.db)

	done := make(chan struct{})
	ac.done = done
	m.crawls[sessionID] = ac

	go func() {
		defer func() {
			m.finishSession(sessionID)
			m.mu.Lock()
			delete(m.crawls, sessionID)
			m.mu.Unlock()
			close(done)
		}()
		m.startSessionUpdater(crawlCtx, sessionID, metrics)
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
func (m *Manager) startSessionUpdater(ctx context.Context, sessionID int64, metrics *Metrics) {
	if m.db == nil || sessionID == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				snap := metrics.Snapshot()
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
	m.mu.Lock()
	ac, ok := m.crawls[sessionID]
	m.mu.Unlock()
	if !ok {
		return
	}

	if m.db == nil || sessionID == 0 {
		return
	}
	snap := ac.metrics.Snapshot()
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

	// For stopped sessions, record how many queue tasks were saved for resume
	if status == "stopped" {
		if count, err := m.db.CountSessionQueueTasks(sessionID); err == nil && count > 0 {
			m.db.UpdateSessionSavedQueueCount(sessionID, count)
		}
	}
}

// StopCrawl cancels all running crawls.
func (m *Manager) StopCrawl() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ac := range m.crawls {
		ac.cancelFn()
	}
}

// StopCrawlByID cancels a specific running crawl by its session ID.
func (m *Manager) StopCrawlByID(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ac, ok := m.crawls[id]; ok {
		ac.cancelFn()
		return true
	}
	return false
}

// RemoveActiveCrawl forcefully removes a crawl from the active list.
// This is called after a crawl has been stopped and cleaned up.
func (m *Manager) RemoveActiveCrawl(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.crawls, id)
}

// ResumeCrawlByID resumes a previously stopped crawl using its per-session saved state.
func (m *Manager) ResumeCrawlByID(sessionID int64) (int64, <-chan struct{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.db == nil {
		return 0, nil, errors.New("no database configured for resume")
	}

	// Load session record and verify it's stopped
	sess, err := m.db.LoadCrawlSession(sessionID)
	if err != nil {
		return 0, nil, fmt.Errorf("load session: %w", err)
	}
	if sess.Status != "stopped" {
		return 0, nil, fmt.Errorf("session status is %q, not stopped", sess.Status)
	}

	// If session is in running map but DB says stopped, force remove it
	// (happens when goroutine is still cleaning up after stop)
	if _, ok := m.crawls[sessionID]; ok {
		delete(m.crawls, sessionID)
	}

	// Load per-session visited URLs and queue
	visited, err := m.db.LoadSessionVisitedURLs(sessionID)
	if err != nil {
		return 0, nil, fmt.Errorf("load visited URLs: %w", err)
	}
	queuedRows, err := m.db.LoadSessionQueuedTasks(sessionID)
	if err != nil {
		return 0, nil, fmt.Errorf("load queued tasks: %w", err)
	}

	var queue []CrawlTask
	if len(queuedRows) == 0 {
		// Queue was empty — add seed URL back to queue so crawl can continue
		// discovering new links (visited set still prevents re-fetching old pages)
		queue = append(queue, CrawlTask{URL: sess.OriginURL, Depth: 0})
		// DON'T delete from visited — keep already-visited URLs cached
	} else {
		for _, qt := range queuedRows {
			queue = append(queue, CrawlTask{URL: qt.URL, OriginURL: qt.OriginURL, Depth: qt.Depth})
		}
	}

	// Build config from session record
	cfg := Config{
		SeedURL:        sess.OriginURL,
		MaxDepth:       sess.MaxDepth,
		MaxURLs:        sess.MaxURLs,
		NumWorkers:     sess.NumWorkers,
		QueueSize:      sess.QueueSize,
		RequestTimeout: 10 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     sess.SameDomain,
	}

	// Mark session as running again
	m.db.UpdateSessionStatus(sessionID, "running")
	m.db.UpdateSessionSavedQueueCount(sessionID, 0)

	metrics := &Metrics{}
	// Restore counters from the stopped session so dashboard shows cumulative stats
	metrics.PagesProcessed.Store(sess.VisitedCount)
	metrics.IndexedDocs.Store(sess.IndexedCount)
	metrics.PagesErrored.Store(sess.ErrorCount)
	metrics.PagesQueued.Store(sess.QueuedCount)
	crawlCtx, cancelFn := context.WithCancel(m.parentCtx)

	ac := &activeCrawl{
		sessionID: sessionID,
		metrics:   metrics,
		cancelFn:  cancelFn,
		seedURL:   sess.OriginURL,
	}

	c := NewCrawler(cfg, sessionID, m.idx, metrics, m.db)

	done := make(chan struct{})
	ac.done = done
	m.crawls[sessionID] = ac

	go func() {
		defer func() {
			m.finishSession(sessionID)
			m.mu.Lock()
			delete(m.crawls, sessionID)
			m.mu.Unlock()
			close(done)
		}()
		m.startSessionUpdater(crawlCtx, sessionID, metrics)
		c.Start(crawlCtx, &ResumeState{Visited: visited, Queue: queue})
	}()

	return sessionID, done, nil
}

// IsRunning reports whether any crawl is currently in progress.
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.crawls) > 0
}

// RunningCount returns the number of currently running crawls.
func (m *Manager) RunningCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.crawls)
}

// GetMetrics returns the metrics for the most recently started crawl.
// Returns a zeroed Metrics if no crawl has run. Never returns nil.
func (m *Manager) GetMetrics() *Metrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *activeCrawl
	for _, ac := range m.crawls {
		if latest == nil || ac.sessionID > latest.sessionID {
			latest = ac
		}
	}
	if latest != nil {
		return latest.metrics
	}
	return &Metrics{}
}

// GetRunningCrawls returns a snapshot of all currently running crawls.
func (m *Manager) GetRunningCrawls() []RunningCrawlInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]RunningCrawlInfo, 0, len(m.crawls))
	for _, ac := range m.crawls {
		snap := ac.metrics.Snapshot()
		snap.UptimeStr = FormatUptime(snap.Uptime)
		result = append(result, RunningCrawlInfo{
			SessionID: ac.sessionID,
			SeedURL:   ac.seedURL,
			Metrics:   snap,
		})
	}
	return result
}

// GetIndex returns the shared Index. Stable across crawls.
func (m *Manager) GetIndex() *index.Index {
	return m.idx
}

// Done returns a channel that closes when a specific crawl finishes.
// Returns nil if the session is not running.
func (m *Manager) Done() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Return the done channel of the most recently started crawl for backward compat
	var latest *activeCrawl
	for _, ac := range m.crawls {
		if latest == nil || ac.sessionID > latest.sessionID {
			latest = ac
		}
	}
	if latest != nil {
		return latest.done
	}
	return nil
}

// DoneByID returns a channel that closes when the given crawl finishes.
func (m *Manager) DoneByID(id int64) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ac, ok := m.crawls[id]; ok {
		return ac.done
	}
	return nil
}

// GetDB returns the storage database (may be nil).
func (m *Manager) GetDB() *storage.DB {
	return m.db
}

// ActiveSessionID returns the DB id of the most recently started crawl (0 if none).
func (m *Manager) ActiveSessionID() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var maxID int64
	for id := range m.crawls {
		if id > maxID {
			maxID = id
		}
	}
	return maxID
}

// ActiveSessionIDs returns all running session IDs.
func (m *Manager) ActiveSessionIDs() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]int64, 0, len(m.crawls))
	for id := range m.crawls {
		ids = append(ids, id)
	}
	return ids
}
