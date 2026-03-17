package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection for crawl persistence.
type DB struct {
	conn *sql.DB
	path string
}

// CrawlState holds metadata about a persisted crawl.
type CrawlState struct {
	SeedURL    string
	MaxDepth   int
	NumWorkers int
	SameDomain bool
}

// QueuedTask represents a URL still waiting to be crawled.
type QueuedTask struct {
	URL       string
	OriginURL string
	Depth     int
}

// CrawlSession represents a historical crawl record.
type CrawlSession struct {
	ID              int64  `json:"id"`
	OriginURL       string `json:"origin_url"`
	MaxDepth        int    `json:"max_depth"`
	MaxURLs         int    `json:"max_urls"`
	NumWorkers      int    `json:"num_workers"`
	QueueSize       int    `json:"queue_size"`
	SameDomain      bool   `json:"same_domain"`
	Status          string `json:"status"` // queued, running, completed, stopped, failed
	VisitedCount    int64  `json:"visited_count"`
	QueuedCount     int64  `json:"queued_count"`
	IndexedCount    int64  `json:"indexed_count"`
	ErrorCount      int64  `json:"error_count"`
	StopReason      string `json:"stop_reason,omitempty"`
	ErrorMessage    string `json:"error_message,omitempty"`
	StartedAt       string `json:"started_at"`
	FinishedAt      string `json:"finished_at,omitempty"`
	SavedQueueCount int64  `json:"saved_queue_count"`
}

// Open creates or opens a SQLite database at the given path.
func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	// Use _pragma DSN params so every pooled connection gets the same settings.
	// WAL allows concurrent reads while writing. busy_timeout waits instead of failing.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db := &DB{conn: conn, path: path}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// Close closes the database.
func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS crawl_state (
		id          INTEGER PRIMARY KEY CHECK (id = 1),
		seed_url    TEXT NOT NULL,
		max_depth   INTEGER NOT NULL,
		num_workers INTEGER NOT NULL,
		same_domain INTEGER NOT NULL DEFAULT 0,
		completed   INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS visited_urls (
		url TEXT PRIMARY KEY
	);

	CREATE TABLE IF NOT EXISTS queue (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		url       TEXT NOT NULL,
		origin_url TEXT NOT NULL DEFAULT '',
		depth     INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS documents (
		url        TEXT PRIMARY KEY,
		origin_url TEXT NOT NULL DEFAULT '',
		depth      INTEGER NOT NULL,
		title      TEXT NOT NULL DEFAULT '',
		body_text  TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE IF NOT EXISTS postings (
		token     TEXT NOT NULL,
		url       TEXT NOT NULL,
		origin_url TEXT NOT NULL DEFAULT '',
		depth     INTEGER NOT NULL,
		title     TEXT NOT NULL DEFAULT '',
		term_freq INTEGER NOT NULL DEFAULT 0,
		in_title  INTEGER NOT NULL DEFAULT 0,
		in_url    INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (token, url)
	);

	CREATE INDEX IF NOT EXISTS idx_postings_token ON postings(token);

	CREATE TABLE IF NOT EXISTS crawl_sessions (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		origin_url    TEXT NOT NULL,
		max_depth     INTEGER NOT NULL,
		max_urls      INTEGER NOT NULL DEFAULT 0,
		num_workers   INTEGER NOT NULL,
		queue_size    INTEGER NOT NULL DEFAULT 10000,
		same_domain   INTEGER NOT NULL DEFAULT 0,
		status        TEXT NOT NULL DEFAULT 'queued',
		visited_count INTEGER NOT NULL DEFAULT 0,
		queued_count  INTEGER NOT NULL DEFAULT 0,
		indexed_count INTEGER NOT NULL DEFAULT 0,
		error_count   INTEGER NOT NULL DEFAULT 0,
		stop_reason   TEXT NOT NULL DEFAULT '',
		error_message TEXT NOT NULL DEFAULT '',
		started_at    TEXT NOT NULL DEFAULT '',
		finished_at   TEXT NOT NULL DEFAULT ''
	);
	`
	_, err := db.conn.Exec(schema)
	if err != nil {
		return err
	}

	// Per-session resume migration (idempotent).
	alterStmts := []string{
		`ALTER TABLE queue ADD COLUMN session_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE crawl_sessions ADD COLUMN saved_queue_count INTEGER NOT NULL DEFAULT 0`,
	}
	for _, stmt := range alterStmts {
		_, err := db.conn.Exec(stmt)
		if err != nil && !strings.Contains(err.Error(), "duplicate column") {
			// Ignore "duplicate column" — means column already exists.
			return err
		}
	}

	_, err = db.conn.Exec(`
		CREATE TABLE IF NOT EXISTS session_visited_urls (
			session_id INTEGER NOT NULL,
			url        TEXT NOT NULL,
			PRIMARY KEY (session_id, url)
		);
		CREATE INDEX IF NOT EXISTS idx_queue_session ON queue(session_id);
	`)
	return err
}

// SaveCrawlState persists crawl configuration.
func (db *DB) SaveCrawlState(state CrawlState) error {
	_, err := db.conn.Exec(`
		INSERT OR REPLACE INTO crawl_state (id, seed_url, max_depth, num_workers, same_domain, completed)
		VALUES (1, ?, ?, ?, ?, 0)`,
		state.SeedURL, state.MaxDepth, state.NumWorkers, boolToInt(state.SameDomain))
	return err
}

// MarkCrawlComplete marks the crawl as finished.
func (db *DB) MarkCrawlComplete() error {
	_, err := db.conn.Exec(`UPDATE crawl_state SET completed = 1 WHERE id = 1`)
	return err
}

// LoadCrawlState loads the persisted crawl state. Returns nil if none exists.
func (db *DB) LoadCrawlState() (*CrawlState, bool, error) {
	var state CrawlState
	var completed int
	var sameDomain int
	err := db.conn.QueryRow(`SELECT seed_url, max_depth, num_workers, same_domain, completed FROM crawl_state WHERE id = 1`).
		Scan(&state.SeedURL, &state.MaxDepth, &state.NumWorkers, &sameDomain, &completed)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	state.SameDomain = sameDomain != 0
	return &state, completed != 0, nil
}

// AddVisitedURL marks a URL as visited.
func (db *DB) AddVisitedURL(url string) error {
	_, err := db.conn.Exec(`INSERT OR IGNORE INTO visited_urls (url) VALUES (?)`, url)
	return err
}

// AddVisitedURLs marks multiple URLs as visited in a single transaction.
func (db *DB) AddVisitedURLs(urls []string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO visited_urls (url) VALUES (?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, u := range urls {
		if _, err := stmt.Exec(u); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadVisitedURLs returns the full set of visited URLs.
func (db *DB) LoadVisitedURLs() (map[string]bool, error) {
	rows, err := db.conn.Query(`SELECT url FROM visited_urls`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	visited := make(map[string]bool)
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		visited[u] = true
	}
	return visited, rows.Err()
}

// SaveQueuedTasks persists the remaining queue for resume.
func (db *DB) SaveQueuedTasks(tasks []QueuedTask) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM queue`); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`INSERT INTO queue (url, origin_url, depth) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, t := range tasks {
		if _, err := stmt.Exec(t.URL, t.OriginURL, t.Depth); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadQueuedTasks loads the persisted queue.
func (db *DB) LoadQueuedTasks() ([]QueuedTask, error) {
	rows, err := db.conn.Query(`SELECT url, origin_url, depth FROM queue ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []QueuedTask
	for rows.Next() {
		var t QueuedTask
		if err := rows.Scan(&t.URL, &t.OriginURL, &t.Depth); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// SavePosting persists a single index posting.
func (db *DB) SavePosting(token, url, originURL string, depth int, title string, termFreq int, inTitle, inURL bool) error {
	_, err := db.conn.Exec(`
		INSERT OR REPLACE INTO postings (token, url, origin_url, depth, title, term_freq, in_title, in_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		token, url, originURL, depth, title, termFreq, boolToInt(inTitle), boolToInt(inURL))
	return err
}

// SavePostingsBatch persists multiple postings in a single transaction.
func (db *DB) SavePostingsBatch(postings []PostingRow) error {
	if len(postings) == 0 {
		return nil
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO postings (token, url, origin_url, depth, title, term_freq, in_title, in_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, p := range postings {
		if _, err := stmt.Exec(p.Token, p.URL, p.OriginURL, p.Depth, p.Title, p.TermFreq, boolToInt(p.InTitle), boolToInt(p.InURL)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PostingRow is a flat representation of a posting for batch operations.
type PostingRow struct {
	Token     string
	URL       string
	OriginURL string
	Depth     int
	Title     string
	TermFreq  int
	InTitle   bool
	InURL     bool
}

// LoadAllPostings loads all postings from the database to rebuild the in-memory index.
func (db *DB) LoadAllPostings() ([]PostingRow, error) {
	rows, err := db.conn.Query(`SELECT token, url, origin_url, depth, title, term_freq, in_title, in_url FROM postings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []PostingRow
	for rows.Next() {
		var p PostingRow
		var inTitle, inURL int
		if err := rows.Scan(&p.Token, &p.URL, &p.OriginURL, &p.Depth, &p.Title, &p.TermFreq, &inTitle, &inURL); err != nil {
			return nil, err
		}
		p.InTitle = inTitle != 0
		p.InURL = inURL != 0
		result = append(result, p)
	}
	return result, rows.Err()
}

// CountDocuments returns the number of unique documents in the postings table.
func (db *DB) CountDocuments() (int, error) {
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(DISTINCT url) FROM postings`).Scan(&count)
	return count, err
}

// ClearAll removes all data for a fresh crawl.
func (db *DB) ClearAll() error {
	_, err := db.conn.Exec(`
		DELETE FROM crawl_state;
		DELETE FROM visited_urls;
		DELETE FROM queue;
		DELETE FROM documents;
		DELETE FROM postings;
	`)
	return err
}

// --- CrawlSession CRUD ---

// CreateCrawlSession inserts a new crawl session record and returns its ID.
func (db *DB) CreateCrawlSession(s CrawlSession) (int64, error) {
	res, err := db.conn.Exec(`
		INSERT INTO crawl_sessions (origin_url, max_depth, max_urls, num_workers, queue_size, same_domain, status, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.OriginURL, s.MaxDepth, s.MaxURLs, s.NumWorkers, s.QueueSize, boolToInt(s.SameDomain), s.Status, s.StartedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateCrawlSessionMetrics updates the live counters for a running crawl.
func (db *DB) UpdateCrawlSessionMetrics(id int64, visited, queued, indexed, errors int64) error {
	_, err := db.conn.Exec(`
		UPDATE crawl_sessions SET visited_count = ?, queued_count = ?, indexed_count = ?, error_count = ?
		WHERE id = ?`, visited, queued, indexed, errors, id)
	return err
}

// FinishCrawlSession marks a session as finished with the given status and reason.
func (db *DB) FinishCrawlSession(id int64, status, stopReason, finishedAt string, visited, queued, indexed, errors int64) error {
	_, err := db.conn.Exec(`
		UPDATE crawl_sessions SET status = ?, stop_reason = ?, finished_at = ?,
		visited_count = ?, queued_count = ?, indexed_count = ?, error_count = ?
		WHERE id = ?`, status, stopReason, finishedAt, visited, queued, indexed, errors, id)
	return err
}

// LoadAllCrawlSessions returns all crawl sessions ordered by most recent first.
func (db *DB) LoadAllCrawlSessions() ([]CrawlSession, error) {
	rows, err := db.conn.Query(`
		SELECT id, origin_url, max_depth, max_urls, num_workers, queue_size, same_domain,
		       status, visited_count, queued_count, indexed_count, error_count,
		       stop_reason, error_message, started_at, finished_at, saved_queue_count
		FROM crawl_sessions ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []CrawlSession
	for rows.Next() {
		var s CrawlSession
		var sameDomain int
		if err := rows.Scan(&s.ID, &s.OriginURL, &s.MaxDepth, &s.MaxURLs, &s.NumWorkers,
			&s.QueueSize, &sameDomain, &s.Status, &s.VisitedCount, &s.QueuedCount,
			&s.IndexedCount, &s.ErrorCount, &s.StopReason, &s.ErrorMessage,
			&s.StartedAt, &s.FinishedAt, &s.SavedQueueCount); err != nil {
			return nil, err
		}
		s.SameDomain = sameDomain != 0
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// LoadCrawlSession loads a single crawl session by ID.
func (db *DB) LoadCrawlSession(id int64) (*CrawlSession, error) {
	var s CrawlSession
	var sameDomain int
	err := db.conn.QueryRow(`
		SELECT id, origin_url, max_depth, max_urls, num_workers, queue_size, same_domain,
		       status, visited_count, queued_count, indexed_count, error_count,
		       stop_reason, error_message, started_at, finished_at, saved_queue_count
		FROM crawl_sessions WHERE id = ?`, id).
		Scan(&s.ID, &s.OriginURL, &s.MaxDepth, &s.MaxURLs, &s.NumWorkers,
			&s.QueueSize, &sameDomain, &s.Status, &s.VisitedCount, &s.QueuedCount,
			&s.IndexedCount, &s.ErrorCount, &s.StopReason, &s.ErrorMessage,
			&s.StartedAt, &s.FinishedAt, &s.SavedQueueCount)
	if err != nil {
		return nil, err
	}
	s.SameDomain = sameDomain != 0
	return &s, nil
}

// DeleteCompletedCrawlSessions removes all non-running sessions and their resume data.
func (db *DB) DeleteCompletedCrawlSessions() error {
	_, _ = db.conn.Exec(`DELETE FROM queue WHERE session_id IN
		(SELECT id FROM crawl_sessions WHERE status NOT IN ('running', 'queued'))`)
	_, _ = db.conn.Exec(`DELETE FROM session_visited_urls WHERE session_id IN
		(SELECT id FROM crawl_sessions WHERE status NOT IN ('running', 'queued'))`)
	_, err := db.conn.Exec(`DELETE FROM crawl_sessions WHERE status NOT IN ('running', 'queued')`)
	return err
}

// CountWordTokens returns the number of unique tokens in the postings table.
func (db *DB) CountWordTokens() (int64, error) {
	var count int64
	err := db.conn.QueryRow(`SELECT COUNT(DISTINCT token) FROM postings`).Scan(&count)
	return count, err
}

// --- Per-session resume methods ---

// SaveSessionQueuedTasks persists queue tasks scoped to a session.
func (db *DB) SaveSessionQueuedTasks(sessionID int64, tasks []QueuedTask) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM queue WHERE session_id = ?`, sessionID); err != nil {
		return err
	}

	stmt, err := tx.Prepare(`INSERT INTO queue (url, origin_url, depth, session_id) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, t := range tasks {
		if _, err := stmt.Exec(t.URL, t.OriginURL, t.Depth, sessionID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadSessionQueuedTasks loads the persisted queue for a specific session.
func (db *DB) LoadSessionQueuedTasks(sessionID int64) ([]QueuedTask, error) {
	rows, err := db.conn.Query(`SELECT url, origin_url, depth FROM queue WHERE session_id = ? ORDER BY id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []QueuedTask
	for rows.Next() {
		var t QueuedTask
		if err := rows.Scan(&t.URL, &t.OriginURL, &t.Depth); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// AddSessionVisitedURLs marks multiple URLs as visited for a specific session.
func (db *DB) AddSessionVisitedURLs(sessionID int64, urls []string) error {
	if len(urls) == 0 {
		return nil
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO session_visited_urls (session_id, url) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, u := range urls {
		if _, err := stmt.Exec(sessionID, u); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadSessionVisitedURLs returns the set of visited URLs for a specific session.
func (db *DB) LoadSessionVisitedURLs(sessionID int64) (map[string]bool, error) {
	rows, err := db.conn.Query(`SELECT url FROM session_visited_urls WHERE session_id = ?`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	visited := make(map[string]bool)
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		visited[u] = true
	}
	return visited, rows.Err()
}

// ClearSessionResumeData removes queue and visited data for a session.
func (db *DB) ClearSessionResumeData(sessionID int64) error {
	_, _ = db.conn.Exec(`DELETE FROM queue WHERE session_id = ?`, sessionID)
	_, err := db.conn.Exec(`DELETE FROM session_visited_urls WHERE session_id = ?`, sessionID)
	return err
}

// UpdateSessionStatus updates the status of a crawl session.
func (db *DB) UpdateSessionStatus(id int64, status string) error {
	_, err := db.conn.Exec(`UPDATE crawl_sessions SET status = ?, finished_at = '' WHERE id = ?`, status, id)
	return err
}

// UpdateSessionSavedQueueCount records how many queue tasks were saved for resume.
func (db *DB) UpdateSessionSavedQueueCount(id int64, count int64) error {
	_, err := db.conn.Exec(`UPDATE crawl_sessions SET saved_queue_count = ? WHERE id = ?`, count, id)
	return err
}

// CountSessionQueueTasks returns the number of saved queue tasks for a session.
func (db *DB) CountSessionQueueTasks(sessionID int64) (int64, error) {
	var count int64
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM queue WHERE session_id = ?`, sessionID).Scan(&count)
	return count, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
