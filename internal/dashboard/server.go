package dashboard

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ege/google-in-a-day/internal/crawler"
	"github.com/ege/google-in-a-day/internal/index"
	"github.com/ege/google-in-a-day/internal/storage"
)

// Server provides the web dashboard and search API.
type Server struct {
	port    int
	manager *crawler.Manager
	tmpl    *template.Template
}

// NewServer creates a dashboard server.
func NewServer(port int, manager *crawler.Manager) *Server {
	return &Server{
		port:    port,
		manager: manager,
	}
}

// Start begins serving HTTP. Blocks until the server is shut down.
func (s *Server) Start() error {
	var err error
	s.tmpl, err = template.New("dashboard").Parse(dashboardHTML)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	mux := http.NewServeMux()

	// Pages
	mux.HandleFunc("/", s.handlePage)

	// JSON APIs
	mux.HandleFunc("/api/metrics", s.handleMetrics)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/crawls", s.handleCrawls)
	mux.HandleFunc("/api/crawls/", s.handleCrawlByID)
	mux.HandleFunc("/api/crawls/completed", s.handleClearCompleted)

	// Legacy API compatibility
	mux.HandleFunc("/api/index", s.handleStartCrawlJSON)
	mux.HandleFunc("/api/stop", s.handleStopCrawlJSON)

	addr := fmt.Sprintf(":%d", s.port)
	fmt.Printf("Dashboard: http://localhost%s\n", addr)
	return http.ListenAndServe(addr, mux)
}

// ---------- Page handler (single HTML template, tab-based) ----------

// waitForAllCrawls waits up to 5 seconds for all active crawls to finish.
func (s *Server) waitForAllCrawls() {
	for _, id := range s.manager.ActiveSessionIDs() {
		if done := s.manager.DoneByID(id); done != nil {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// parseSeedURLs splits a multi-line textarea input into valid seed URLs.
func parseSeedURLs(raw string) []string {
	lines := strings.Split(raw, "\n")
	var seeds []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Prepend https:// if user typed a bare domain
		if !strings.HasPrefix(line, "http://") && !strings.HasPrefix(line, "https://") {
			line = "https://" + line
		}
		if u, err := url.Parse(line); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			seeds = append(seeds, line)
		}
	}
	return seeds
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	// Handle POST actions (create crawl, stop crawl, clear, resume)
	if r.Method == http.MethodPost {
		action := r.FormValue("action")
		switch action {
		case "stop":
			s.manager.StopCrawl()
			// Wait for all crawls to finish cleanup (goroutines call finishSession)
			s.waitForAllCrawls()
			// Fallback: mark any still-running sessions as stopped in DB
			if db := s.manager.GetDB(); db != nil {
				for _, id := range s.manager.ActiveSessionIDs() {
					db.UpdateSessionStatus(id, "stopped")
				}
			}
		case "stop_crawl":
			if id, err := strconv.ParseInt(r.FormValue("crawl_id"), 10, 64); err == nil {
				s.manager.StopCrawlByID(id)
				// Wait for goroutine to finish (it will call finishSession + cleanup)
				done := s.manager.DoneByID(id)
				if done != nil {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
					}
				}
				// Ensure DB status is stopped (in case goroutine didn't finish in time)
				if db := s.manager.GetDB(); db != nil {
					db.UpdateSessionStatus(id, "stopped")
				}
			}
			// Redirect to status tab so user sees the Resume button
			http.Redirect(w, r, "/?tab=status", http.StatusSeeOther)
			return
		case "resume_crawl":
			if id, err := strconv.ParseInt(r.FormValue("crawl_id"), 10, 64); err == nil {
				_, _, err := s.manager.ResumeCrawlByID(id)
				if err != nil {
					log.Printf("resume error: %v", err)
				}
			}
		case "clear":
			if db := s.manager.GetDB(); db != nil {
				db.DeleteCompletedCrawlSessions()
			}
		case "start":
			seeds := parseSeedURLs(r.FormValue("seed"))
			if len(seeds) > 0 {
				depth := 3
				if d, err := strconv.Atoi(r.FormValue("depth")); err == nil && d > 0 {
					depth = d
				}
				workers := 5
				if wv, err := strconv.Atoi(r.FormValue("workers")); err == nil && wv > 0 {
					workers = wv
				}
				queueSize := 10000
				if q, err := strconv.Atoi(r.FormValue("queue_size")); err == nil && q > 0 {
					queueSize = q
				}
				maxURLs := 0
				if mv, err := strconv.Atoi(r.FormValue("max_urls")); err == nil && mv > 0 {
					maxURLs = mv
				}
				sameDomain := r.FormValue("same_domain") == "on"

				// Each seed URL starts its own independent crawl
				for _, seed := range seeds {
					cfg := crawler.Config{
						SeedURL:        seed,
						MaxDepth:       depth,
						MaxURLs:        maxURLs,
						NumWorkers:     workers,
						QueueSize:      queueSize,
						RequestTimeout: 10 * time.Second,
						MaxBodySize:    1 << 20,
						SameDomain:     sameDomain,
					}
					_, _, err := s.manager.StartCrawl(cfg)
					if err != nil {
						log.Printf("error starting crawl for %s: %v", seed, err)
					}
				}
			}
		}
		// PRG redirect — stop actions go to status tab (for resume), others stay
		tab := r.FormValue("tab")
		if tab == "" {
			tab = "create"
		}
		if action == "stop_crawl" || action == "stop" {
			tab = "status"
		}
		http.Redirect(w, r, "/?tab="+tab, http.StatusSeeOther)
		return
	}

	// GET: determine which tab is active
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "search"
	}

	metrics := s.manager.GetMetrics()
	snap := metrics.Snapshot()
	snap.UptimeStr = crawler.FormatUptime(snap.Uptime)

	isRunning := s.manager.IsRunning()

	// Load crawl sessions from DB
	var sessions []storage.CrawlSession
	// Update running sessions with live metrics
	runningCrawls := s.manager.GetRunningCrawls()
	if db := s.manager.GetDB(); db != nil {
		var err error
		sessions, err = db.LoadAllCrawlSessions()
		if err != nil {
			log.Printf("Warning: failed to load crawl sessions: %v", err)
		}
		// Build a map of running crawl metrics by session ID
		runningMap := make(map[int64]crawler.MetricsSnapshot)
		for _, rc := range runningCrawls {
			runningMap[rc.SessionID] = rc.Metrics
		}
		for i := range sessions {
			if m, ok := runningMap[sessions[i].ID]; ok {
				sessions[i].VisitedCount = m.PagesProcessed
				sessions[i].QueuedCount = m.PagesQueued
				sessions[i].IndexedCount = m.IndexedDocs
				sessions[i].ErrorCount = m.PagesErrored
			}
		}
	}

	// Summary stats
	var totalVisited, totalWords int64
	var activeCount, totalCount int
	if db := s.manager.GetDB(); db != nil {
		totalCount = len(sessions)
		for _, sess := range sessions {
			totalVisited += sess.VisitedCount
			if sess.Status == "running" {
				activeCount++
			}
		}
		totalWords, _ = db.CountWordTokens()
	}

	data := struct {
		Tab           string
		Metrics       crawler.MetricsSnapshot
		Query         string
		Results       []index.SearchResult
		IsRunning     bool
		RunningCrawls []crawler.RunningCrawlInfo
		Sessions      []storage.CrawlSession
		TotalVisited  int64
		TotalWords    int64
		ActiveCount   int
		TotalCount    int
	}{
		Tab:           tab,
		Metrics:       snap,
		IsRunning:     isRunning,
		RunningCrawls: runningCrawls,
		Sessions:      sessions,
		TotalVisited:  totalVisited,
		TotalWords:    totalWords,
		ActiveCount:   activeCount,
		TotalCount:    totalCount,
	}

	q := r.URL.Query().Get("q")
	if q != "" {
		data.Query = q
		data.Results = index.Search(q, s.manager.GetIndex(), 20)
		data.Tab = "search"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	s.tmpl.Execute(w, data)
}

// ---------- JSON API handlers ----------

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	metrics := s.manager.GetMetrics()
	snap := metrics.Snapshot()
	snap.UptimeStr = crawler.FormatUptime(snap.Uptime)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snap)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	topK := 20
	if k := r.URL.Query().Get("k"); k != "" {
		if n, err := strconv.Atoi(k); err == nil && n > 0 {
			topK = n
		}
	}
	results := index.Search(q, s.manager.GetIndex(), topK)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// handleCrawls: GET = list all sessions, POST = create a new crawl
func (s *Server) handleCrawls(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		db := s.manager.GetDB()
		if db == nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]storage.CrawlSession{})
			return
		}
		sessions, err := db.LoadAllCrawlSessions()
		if err != nil {
			http.Error(w, `{"error":"failed to load sessions"}`, http.StatusInternalServerError)
			return
		}
		if sessions == nil {
			sessions = []storage.CrawlSession{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessions)

	case http.MethodPost:
		var req struct {
			Seed       string `json:"seed"`
			Depth      int    `json:"depth"`
			MaxURLs    int    `json:"max_urls"`
			Workers    int    `json:"workers"`
			QueueSize  int    `json:"queue_size"`
			SameDomain bool   `json:"same_domain"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
			return
		}
		if req.Seed == "" {
			http.Error(w, `{"error":"seed is required"}`, http.StatusBadRequest)
			return
		}
		if req.Depth <= 0 {
			req.Depth = 3
		}
		if req.Workers <= 0 {
			req.Workers = 5
		}
		if req.QueueSize <= 0 {
			req.QueueSize = 10000
		}

		cfg := crawler.Config{
			SeedURL:        req.Seed,
			MaxDepth:       req.Depth,
			MaxURLs:        req.MaxURLs,
			NumWorkers:     req.Workers,
			QueueSize:      req.QueueSize,
			RequestTimeout: 10 * time.Second,
			MaxBodySize:    1 << 20,
			SameDomain:     req.SameDomain,
		}

		sessionID, _, err := s.manager.StartCrawl(cfg)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "started", "id": sessionID})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleCrawlByID: GET /api/crawls/{id}, POST /api/crawls/{id}/stop
func (s *Server) handleCrawlByID(w http.ResponseWriter, r *http.Request) {
	// Parse path: /api/crawls/{id} or /api/crawls/{id}/stop
	path := strings.TrimPrefix(r.URL.Path, "/api/crawls/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "missing crawl id", http.StatusBadRequest)
		return
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid crawl id", http.StatusBadRequest)
		return
	}

	// POST /api/crawls/{id}/stop
	if len(parts) >= 2 && parts[1] == "stop" && r.Method == http.MethodPost {
		if s.manager.StopCrawlByID(id) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": "crawl is not running"})
		}
		return
	}

	// POST /api/crawls/{id}/resume
	if len(parts) >= 2 && parts[1] == "resume" && r.Method == http.MethodPost {
		_, _, err := s.manager.ResumeCrawlByID(id)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		} else {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"status": "resumed", "id": id})
		}
		return
	}

	// GET /api/crawls/{id}
	if r.Method == http.MethodGet {
		db := s.manager.GetDB()
		if db == nil {
			http.Error(w, "no database", http.StatusInternalServerError)
			return
		}
		session, err := db.LoadCrawlSession(id)
		if err != nil {
			http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(session)
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// handleClearCompleted: DELETE /api/crawls/completed
func (s *Server) handleClearCompleted(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	db := s.manager.GetDB()
	if db != nil {
		db.DeleteCompletedCrawlSessions()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "cleared"})
}

// Legacy JSON APIs
func (s *Server) handleStartCrawlJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Seed       string `json:"seed"`
		Depth      int    `json:"depth"`
		Workers    int    `json:"workers"`
		SameDomain bool   `json:"same_domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Seed == "" {
		http.Error(w, `{"error":"seed is required"}`, http.StatusBadRequest)
		return
	}
	if req.Depth <= 0 {
		req.Depth = 3
	}
	if req.Workers <= 0 {
		req.Workers = 5
	}

	cfg := crawler.Config{
		SeedURL:        req.Seed,
		MaxDepth:       req.Depth,
		NumWorkers:     req.Workers,
		QueueSize:      10000,
		RequestTimeout: 10 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     req.SameDomain,
	}

	_, _, err := s.manager.StartCrawl(cfg)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

func (s *Server) handleStopCrawlJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !s.manager.IsRunning() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "no crawl is running"})
		return
	}

	s.manager.StopCrawl()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

// ---------- Dashboard HTML template ----------

const dashboardHTML = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>Google in a Day</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, monospace; background: #0d1117; color: #c9d1d9; }

        /* Top nav */
        .navbar { display: flex; align-items: center; background: #161b22; border-bottom: 1px solid #30363d; padding: 0 24px; height: 56px; }
        .navbar .logo { color: #58a6ff; font-weight: bold; font-size: 1.15em; margin-right: 32px; white-space: nowrap; }
        .navbar .nav-links { display: flex; gap: 4px; }
        .navbar .nav-links a { color: #8b949e; text-decoration: none; padding: 8px 16px; border-radius: 6px; font-size: 0.9em; transition: background 0.15s; }
        .navbar .nav-links a:hover { background: #21262d; color: #c9d1d9; }
        .navbar .nav-links a.active { background: #1f6feb22; color: #58a6ff; font-weight: 600; }

        /* Summary strip */
        .summary-strip { display: flex; align-items: center; gap: 24px; padding: 14px 24px; background: #161b22; border-bottom: 1px solid #30363d; }
        .summary-item { display: flex; flex-direction: column; }
        .summary-item .label { font-size: 0.7em; color: #8b949e; text-transform: uppercase; letter-spacing: 0.5px; }
        .summary-item .value { font-size: 1.2em; font-weight: bold; color: #58a6ff; }
        .summary-item .value.green { color: #3fb950; }
        .summary-spacer { flex: 1; }
        .clear-btn { padding: 6px 14px; background: #21262d; border: 1px solid #30363d; border-radius: 6px; color: #f85149; font-size: 0.8em; cursor: pointer; }
        .clear-btn:hover { background: #30363d; }

        /* Main container */
        .main { padding: 24px; max-width: 1100px; margin: 0 auto; }

        /* Cards */
        .card { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 24px; margin-bottom: 20px; }
        .card h2 { color: #58a6ff; font-size: 1.15em; margin-bottom: 16px; }

        /* Form styles */
        .field { margin-bottom: 14px; }
        .field label { display: block; color: #8b949e; font-size: 0.8em; margin-bottom: 4px; text-transform: uppercase; letter-spacing: 0.5px; }
        .field input[type="text"],
        .field input[type="number"],
        .field input[type="url"],
        .field textarea { width: 100%; padding: 10px 14px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #c9d1d9; font-size: 1em; font-family: inherit; }
        .field input:focus, .field textarea:focus { border-color: #58a6ff; outline: none; }
        .field .hint { font-size: 0.72em; color: #6e7681; margin-top: 3px; }
        .inline { display: flex; gap: 14px; }
        .inline .field { flex: 1; }
        .checkbox-field { display: flex; align-items: center; gap: 8px; margin-bottom: 14px; }
        .checkbox-field input { width: 16px; height: 16px; }
        .checkbox-field label { color: #c9d1d9; font-size: 0.9em; }

        /* Buttons */
        .btn-primary { padding: 10px 24px; background: #238636; border: 1px solid #2ea043; border-radius: 6px; color: #fff; font-size: 1em; cursor: pointer; font-weight: 600; }
        .btn-primary:hover { background: #2ea043; }
        .btn-danger { padding: 8px 16px; background: #da3633; border: 1px solid #f85149; border-radius: 6px; color: #fff; font-size: 0.9em; cursor: pointer; }
        .btn-danger:hover { background: #f85149; }
        .btn-danger-sm { padding: 4px 10px; background: #da3633; border: 1px solid #f85149; border-radius: 6px; color: #fff; font-size: 0.75em; cursor: pointer; }
        .btn-danger-sm:hover { background: #f85149; }
        .btn-resume-sm { padding: 4px 10px; background: #1f6feb; border: 1px solid #388bfd; border-radius: 6px; color: #fff; font-size: 0.75em; cursor: pointer; }
        .btn-resume-sm:hover { background: #388bfd; }

        /* Search */
        .search-box { display: flex; gap: 8px; margin-bottom: 20px; }
        .search-box input[type="text"] { flex: 1; padding: 12px 16px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #c9d1d9; font-size: 1.1em; }
        .search-box input[type="text"]:focus { border-color: #58a6ff; outline: none; }
        .search-box button { padding: 12px 24px; background: #238636; border: 1px solid #2ea043; border-radius: 6px; color: #fff; font-size: 1em; cursor: pointer; }
        .search-box button:hover { background: #2ea043; }

        /* Search results */
        .result { background: #161b22; border: 1px solid #30363d; border-radius: 6px; padding: 14px; margin-bottom: 8px; overflow: hidden; }
        .result .url { color: #58a6ff; font-size: 0.85em; overflow-wrap: break-word; word-break: break-all; }
        .result .url a { color: #58a6ff; text-decoration: none; }
        .result .url a:hover { text-decoration: underline; }
        .result .title { font-weight: bold; margin: 4px 0; overflow-wrap: break-word; }
        .result .meta { color: #8b949e; font-size: 0.8em; overflow-wrap: break-word; }
        .result .meta span { margin-right: 12px; display: inline-block; max-width: 100%; overflow-wrap: break-word; word-break: break-all; }

        /* Metrics grid */
        .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 12px; margin-bottom: 20px; }
        .metric-card { background: #161b22; border: 1px solid #30363d; border-radius: 6px; padding: 14px; }
        .metric-card .label { color: #8b949e; font-size: 0.72em; text-transform: uppercase; letter-spacing: 0.5px; }
        .metric-card .value { font-size: 1.6em; font-weight: bold; color: #58a6ff; margin-top: 2px; }
        .metric-card .value.green { color: #3fb950; }
        .metric-card .value.red { color: #f85149; }

        /* Sessions table */
        .sessions-table { width: 100%; border-collapse: collapse; font-size: 0.85em; }
        .sessions-table th { text-align: left; color: #8b949e; font-size: 0.75em; text-transform: uppercase; letter-spacing: 0.5px; padding: 10px 12px; border-bottom: 1px solid #30363d; }
        .sessions-table td { padding: 10px 12px; border-bottom: 1px solid #21262d; vertical-align: top; }
        .sessions-table tr:hover { background: #161b2299; }
        .status-badge { display: inline-block; padding: 2px 8px; border-radius: 10px; font-size: 0.75em; font-weight: bold; }
        .status-badge.running { background: #1f6feb33; color: #58a6ff; }
        .status-badge.completed { background: #23863633; color: #3fb950; }
        .status-badge.stopped { background: #da363333; color: #f85149; }
        .status-badge.failed { background: #da363333; color: #f85149; }
        .status-badge.queued { background: #30363d; color: #8b949e; }
        .sessions-table .url-cell { max-width: 320px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: #58a6ff; }
        .sessions-table .url-cell a { color: #58a6ff; text-decoration: none; }
        .sessions-table .url-cell a:hover { text-decoration: underline; }
        .sessions-table .meta { color: #8b949e; font-size: 0.8em; line-height: 1.6; }
        .sessions-table .meta span { white-space: nowrap; }
        .sessions-table .stat-group { display: flex; gap: 12px; font-size: 0.9em; }
        .sessions-table .stat-group .stat { display: flex; flex-direction: column; align-items: center; }
        .sessions-table .stat-group .stat .num { font-weight: bold; font-size: 1.1em; }
        .sessions-table .stat-group .stat .lbl { font-size: 0.7em; color: #8b949e; text-transform: uppercase; }
        .sessions-table .time-cell { font-size: 0.78em; color: #8b949e; white-space: nowrap; line-height: 1.6; }

        /* Resume banner */
        .resume-banner { background: #1f6feb22; border: 1px solid #1f6feb; border-radius: 6px; padding: 14px 20px; margin-bottom: 20px; display: flex; align-items: center; justify-content: space-between; }
        .resume-banner .info { color: #58a6ff; font-size: 0.9em; }
        .resume-btn { padding: 8px 16px; background: #1f6feb; border: 1px solid #388bfd; border-radius: 6px; color: #fff; font-size: 0.9em; cursor: pointer; }
        .resume-btn:hover { background: #388bfd; }

        /* Running indicator */
        .running-dot { display: inline-block; width: 8px; height: 8px; background: #58a6ff; border-radius: 50%; margin-right: 6px; animation: pulse 1.5s ease-in-out infinite; }
        @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }

        .empty-state { text-align: center; color: #8b949e; padding: 48px 20px; }
        .empty-state p { margin-bottom: 8px; }

        /* Running crawl card */
        .running-crawl-card { background: #161b22; border: 1px solid #1f6feb55; border-radius: 8px; padding: 16px; margin-bottom: 14px; }
        .running-crawl-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
        .running-crawl-header .seed-url { color: #58a6ff; font-size: 0.85em; overflow-wrap: break-word; word-break: break-all; max-width: 70%; }
        .running-crawl-header .seed-url a { color: #58a6ff; text-decoration: none; }
        .running-crawl-header .seed-url a:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <!-- Top Navigation -->
    <nav class="navbar">
        <span class="logo">Google in a Day</span>
        <div class="nav-links">
            <a href="/?tab=search" {{if eq .Tab "search"}}class="active"{{end}}>Search</a>
            <a href="/?tab=create" {{if eq .Tab "create"}}class="active"{{end}}>Create Crawler</a>
            <a href="/?tab=status" {{if eq .Tab "status"}}class="active"{{end}}>Crawler Status</a>
        </div>
    </nav>

    <!-- Summary Strip -->
    <div class="summary-strip">
        <div class="summary-item">
            <span class="label">URLs Visited</span>
            <span class="value">{{.TotalVisited}}</span>
        </div>
        <div class="summary-item">
            <span class="label">Words in DB</span>
            <span class="value">{{.TotalWords}}</span>
        </div>
        <div class="summary-item">
            <span class="label">Active Crawlers</span>
            <span class="value green">{{.ActiveCount}}</span>
        </div>
        <div class="summary-item">
            <span class="label">Total Created</span>
            <span class="value">{{.TotalCount}}</span>
        </div>
        <span class="summary-spacer"></span>
        {{if .IsRunning}}
            <span style="color: #58a6ff; font-size: 0.85em;"><span class="running-dot"></span>{{.ActiveCount}} Crawling...</span>
            <form method="POST" action="/" style="display:inline">
                <input type="hidden" name="action" value="stop">
                <input type="hidden" name="tab" value="{{.Tab}}">
                <button type="submit" class="btn-danger">Stop All</button>
            </form>
        {{end}}
        <form method="POST" action="/" style="display:inline">
            <input type="hidden" name="action" value="clear">
            <input type="hidden" name="tab" value="{{.Tab}}">
            <button type="submit" class="clear-btn">Clear History</button>
        </form>
    </div>

    <div class="main">

    <!-- ========== SEARCH TAB ========== -->
    {{if eq .Tab "search"}}
    <div style="max-width: 700px; margin: 40px auto 0;">
        <h1 style="text-align:center; color:#58a6ff; font-size:2em; margin-bottom:24px;">Search</h1>
        <form method="GET" action="/" class="search-box">
            <input type="hidden" name="tab" value="search">
            <input type="text" name="q" placeholder="Search indexed pages..." value="{{.Query}}" autofocus>
            <button type="submit">Search</button>
        </form>

        {{if .Query}}
        <p style="color: #8b949e; margin-bottom: 12px;">{{len .Results}} results for "{{.Query}}"</p>
        {{range .Results}}
        <div class="result">
            <div class="url"><a href="{{.URL}}" target="_blank" rel="noopener">{{.URL}}</a></div>
            <div class="title">{{if .Title}}{{.Title}}{{else}}(no title){{end}}</div>
            <div class="meta">
                <span>Score: {{printf "%.2f" .Score}}</span>
                <span>Depth: {{.Depth}}</span>
                <span>Origin: {{if .OriginURL}}<a href="{{.OriginURL}}" target="_blank" rel="noopener" style="color:#8b949e;">{{.OriginURL}}</a>{{else}}(seed){{end}}</span>
            </div>
        </div>
        {{end}}
        {{if not .Results}}
        <p style="color: #8b949e;">No results found.</p>
        {{end}}
        {{else}}
        <div class="empty-state">
            <p style="font-size: 1.1em; color: #c9d1d9;">Enter a query to search across all indexed pages.</p>
            <p>Results show relevant URL, origin URL, and depth as per the assignment spec.</p>
        </div>
        {{end}}
    </div>
    {{end}}

    <!-- ========== CREATE CRAWLER TAB ========== -->
    {{if eq .Tab "create"}}
    <div class="card" style="max-width: 600px; margin: 20px auto;">
        <h2>Create New Crawler</h2>
        <form method="POST" action="/">
            <input type="hidden" name="action" value="start">
            <input type="hidden" name="tab" value="create">
            <div class="field">
                <label>Seed URLs</label>
                <textarea name="seed" rows="3" placeholder="https://example.com&#10;https://another-site.com&#10;(one URL per line)" required></textarea>
                <div class="hint">Enter one or more URLs, one per line. Each URL starts its own independent crawl.</div>
            </div>
            <div class="inline">
                <div class="field">
                    <label>Max Depth</label>
                    <input type="number" name="depth" value="3" min="0" max="50">
                    <div class="hint">Graph traversal distance (hops) from origin.</div>
                </div>
                <div class="field">
                    <label>Max URLs to Visit</label>
                    <input type="number" name="max_urls" value="0" min="0">
                    <div class="hint">Total page count cap. 0 = unlimited.</div>
                </div>
            </div>
            <div class="inline">
                <div class="field">
                    <label>Workers</label>
                    <input type="number" name="workers" value="5" min="1" max="50">
                    <div class="hint">Concurrent crawler goroutines.</div>
                </div>
                <div class="field">
                    <label>Queue Capacity</label>
                    <input type="number" name="queue_size" value="10000" min="100" max="100000">
                    <div class="hint">Bounded task queue size (back-pressure).</div>
                </div>
            </div>
            <div class="checkbox-field">
                <input type="checkbox" name="same_domain" id="same_domain" checked>
                <label for="same_domain">Same domain only</label>
            </div>
            <button type="submit" class="btn-primary">Start Crawler</button>
        </form>
    </div>

    <!-- Live metrics for each running crawl -->
    <div class="running-crawls-container" style="max-width: 600px; margin: 0 auto;">
        {{range .RunningCrawls}}
        <div class="running-crawl-card">
            <div class="running-crawl-header">
                <span class="seed-url"><span class="running-dot"></span><a href="{{.SeedURL}}" target="_blank" rel="noopener">{{.SeedURL}}</a></span>
                <form method="POST" action="/" style="display:inline">
                    <input type="hidden" name="action" value="stop_crawl">
                    <input type="hidden" name="crawl_id" value="{{.SessionID}}">
                    <input type="hidden" name="tab" value="create">
                    <button type="submit" class="btn-danger-sm">Stop</button>
                </form>
            </div>
            <div class="grid">
                <div class="metric-card">
                    <div class="label">Pages Processed</div>
                    <div class="value">{{.Metrics.PagesProcessed}}</div>
                </div>
                <div class="metric-card">
                    <div class="label">Indexed Docs</div>
                    <div class="value green">{{.Metrics.IndexedDocs}}</div>
                </div>
                <div class="metric-card">
                    <div class="label">Errors</div>
                    <div class="value red">{{.Metrics.PagesErrored}}</div>
                </div>
                <div class="metric-card">
                    <div class="label">Uptime</div>
                    <div class="value">{{.Metrics.UptimeStr}}</div>
                </div>
            </div>
        </div>
        {{end}}
    </div>
    {{end}}

    <!-- ========== CRAWLER STATUS TAB ========== -->
    {{if eq .Tab "status"}}
    <h2 style="color: #c9d1d9; margin-bottom: 16px;">Crawler Sessions</h2>

    {{if .Sessions}}
    <div class="card" style="padding: 0;">
        <table class="sessions-table">
            <thead>
                <tr>
                    <th>Origin URL</th>
                    <th>Status</th>
                    <th>Config</th>
                    <th>Stats</th>
                    <th>Time</th>
                    <th>Action</th>
                </tr>
            </thead>
            <tbody>
            {{range .Sessions}}
                <tr>
                    <td class="url-cell" title="{{.OriginURL}}"><a href="{{.OriginURL}}" target="_blank" rel="noopener">{{.OriginURL}}</a></td>
                    <td>
                        <span class="status-badge {{.Status}}">{{.Status}}</span>
                        {{if .StopReason}}<div class="meta" style="margin-top:4px;">{{.StopReason}}</div>{{end}}
                    </td>
                    <td class="meta">
                        <span>depth {{.MaxDepth}}</span> &middot;
                        <span>{{.NumWorkers}}w</span> &middot;
                        <span>q{{.QueueSize}}</span>
                        {{if gt .MaxURLs 0}} &middot; <span>max {{.MaxURLs}}</span>{{end}}
                    </td>
                    <td>
                        <div class="stat-group">
                            <div class="stat"><span class="num">{{.VisitedCount}}</span><span class="lbl">visited</span></div>
                            <div class="stat"><span class="num" style="color:#3fb950;">{{.IndexedCount}}</span><span class="lbl">indexed</span></div>
                            <div class="stat"><span class="num" style="color:#f85149;">{{.ErrorCount}}</span><span class="lbl">errors</span></div>
                        </div>
                    </td>
                    <td class="time-cell">
                        {{.StartedAt}}
                        {{if .FinishedAt}}<br>{{.FinishedAt}}{{end}}
                    </td>
                    <td>
                        {{if eq .Status "running"}}
                        <form method="POST" action="/" style="display:inline">
                            <input type="hidden" name="action" value="stop_crawl">
                            <input type="hidden" name="crawl_id" value="{{.ID}}">
                            <input type="hidden" name="tab" value="status">
                            <button type="submit" class="btn-danger-sm">Stop</button>
                        </form>
                        {{else if eq .Status "stopped"}}
                        <form method="POST" action="/" style="display:inline">
                            <input type="hidden" name="action" value="resume_crawl">
                            <input type="hidden" name="crawl_id" value="{{.ID}}">
                            <input type="hidden" name="tab" value="status">
                            <button type="submit" class="btn-resume-sm">Resume</button>
                        </form>
                        {{else}}-{{end}}
                    </td>
                </tr>
            {{end}}
            </tbody>
        </table>
    </div>
    {{else}}
    <div class="card empty-state">
        <p>No crawl sessions yet.</p>
        <p>Go to <a href="/?tab=create" style="color: #58a6ff;">Create Crawler</a> to start one.</p>
    </div>
    {{end}}
    {{end}}

    </div>

    <script>
    (function() {
        var isRunning = {{if .IsRunning}}true{{else}}false{{end}};
        var currentTab = "{{.Tab}}";
        if (!isRunning) return;

        var pollInterval = 2000;
        var timer = setInterval(function() {
            var fetchURL = "/?tab=" + currentTab + "&_t=" + Date.now();
            if (currentTab === "search") {
                var searchInput = document.querySelector('.search-box input[name="q"]');
                if (searchInput && searchInput.value) {
                    fetchURL += "&q=" + encodeURIComponent(searchInput.value);
                }
            }
            fetch(fetchURL, {headers: {"Accept": "text/html", "Cache-Control": "no-cache"}})
                .then(function(resp) { return resp.text(); })
                .then(function(html) {
                    var parser = new DOMParser();
                    var doc = parser.parseFromString(html, "text/html");

                    // Update summary strip
                    var newStrip = doc.querySelector(".summary-strip");
                    var oldStrip = document.querySelector(".summary-strip");
                    if (newStrip && oldStrip) {
                        oldStrip.innerHTML = newStrip.innerHTML;
                    }

                    // On status and search tabs: replace the entire .main content
                    // (no forms to preserve on these tabs)
                    if (currentTab === "status" || currentTab === "search") {
                        var newMain = doc.querySelector(".main");
                        var oldMain = document.querySelector(".main");
                        if (newMain && oldMain) {
                            oldMain.innerHTML = newMain.innerHTML;
                        }
                    }

                    // On create tab: update running crawl cards only (not the form)
                    if (currentTab === "create") {
                        var container = document.querySelector(".running-crawls-container");
                        var newContainer = doc.querySelector(".running-crawls-container");
                        if (container && newContainer) {
                            container.innerHTML = newContainer.innerHTML;
                        }
                    }

                    // Check if crawling stopped — if no running-dot in fresh HTML, stop polling
                    var stillRunning = doc.querySelector(".running-dot");
                    if (!stillRunning) {
                        clearInterval(timer);
                        location.reload();
                    }
                })
                .catch(function() { /* ignore fetch errors, will retry */ });
        }, pollInterval);
    })();
    </script>
</body>
</html>`
