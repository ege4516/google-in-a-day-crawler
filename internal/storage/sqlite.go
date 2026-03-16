package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

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

// Open creates or opens a SQLite database at the given path.
func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
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
	`
	_, err := db.conn.Exec(schema)
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

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
