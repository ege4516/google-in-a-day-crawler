package dashboard

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/ege/google-in-a-day/internal/crawler"
	"github.com/ege/google-in-a-day/internal/index"
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
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/api/metrics", s.handleMetrics)
	mux.HandleFunc("/api/search", s.handleSearch)
	mux.HandleFunc("/api/index", s.handleStartCrawl)
	mux.HandleFunc("/api/stop", s.handleStopCrawl)

	addr := fmt.Sprintf(":%d", s.port)
	fmt.Printf("Dashboard: http://localhost%s\n", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	// Handle POST: start/stop/resume a crawl via the web form (PRG pattern)
	if r.Method == http.MethodPost {
		action := r.FormValue("action")
		if action == "stop" {
			s.manager.StopCrawl()
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if action == "resume" {
			if state, ok := s.manager.HasResumableState(); ok {
				s.manager.RestoreIndex()
				cfg := crawler.Config{
					SeedURL:        state.SeedURL,
					MaxDepth:       state.MaxDepth,
					NumWorkers:     state.NumWorkers,
					QueueSize:      10000,
					RequestTimeout: 10 * time.Second,
					MaxBodySize:    1 << 20,
					SameDomain:     state.SameDomain,
				}
				s.manager.ResumeCrawl(cfg)
			}
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		seed := r.FormValue("seed")
		if seed == "" {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		depth := 3
		if d, err := strconv.Atoi(r.FormValue("depth")); err == nil && d > 0 {
			depth = d
		}
		workers := 5
		if w, err := strconv.Atoi(r.FormValue("workers")); err == nil && w > 0 {
			workers = w
		}
		sameDomain := r.FormValue("same_domain") == "on"

		cfg := crawler.Config{
			SeedURL:        seed,
			MaxDepth:       depth,
			NumWorkers:     workers,
			QueueSize:      10000,
			RequestTimeout: 10 * time.Second,
			MaxBodySize:    1 << 20,
			SameDomain:     sameDomain,
		}
		s.manager.StartCrawl(cfg)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// GET: render dashboard
	metrics := s.manager.GetMetrics()
	snap := metrics.Snapshot()
	snap.UptimeStr = crawler.FormatUptime(snap.Uptime)

	isRunning := s.manager.IsRunning()
	hasStarted := snap.PagesQueued > 0

	// Check for resumable crawl
	var resumeSeed string
	resumeState, canResume := s.manager.HasResumableState()
	if canResume && !isRunning {
		resumeSeed = resumeState.SeedURL
	}

	data := struct {
		Metrics    crawler.MetricsSnapshot
		Query      string
		Results    []index.SearchResult
		IsRunning  bool
		HasStarted bool
		ShowForm   bool
		CanResume  bool
		ResumeSeed string
	}{
		Metrics:    snap,
		IsRunning:  isRunning,
		HasStarted: hasStarted,
		ShowForm:   !isRunning,
		CanResume:  canResume && !isRunning,
		ResumeSeed: resumeSeed,
	}

	q := r.URL.Query().Get("q")
	if q != "" {
		data.Query = q
		data.Results = index.Search(q, s.manager.GetIndex(), 20)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.tmpl.Execute(w, data)
}

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

func (s *Server) handleStartCrawl(w http.ResponseWriter, r *http.Request) {
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

	_, err := s.manager.StartCrawl(cfg)
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

func (s *Server) handleStopCrawl(w http.ResponseWriter, r *http.Request) {
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

const dashboardHTML = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>Google in a Day — Dashboard</title>
    {{if .IsRunning}}<meta http-equiv="refresh" content="2">{{end}}
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, monospace; background: #0d1117; color: #c9d1d9; padding: 20px; }
        h1 { color: #58a6ff; margin-bottom: 8px; font-size: 1.5em; }
        .subtitle { color: #8b949e; margin-bottom: 20px; font-size: 0.9em; }
        .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 12px; margin-bottom: 24px; }
        .card { background: #161b22; border: 1px solid #30363d; border-radius: 6px; padding: 16px; }
        .card .label { color: #8b949e; font-size: 0.75em; text-transform: uppercase; letter-spacing: 0.5px; }
        .card .value { font-size: 1.8em; font-weight: bold; color: #58a6ff; margin-top: 4px; }
        .card .value.done { color: #3fb950; }
        .card .value.error { color: #f85149; }
        .search-box { margin-bottom: 24px; }
        .search-box form { display: flex; gap: 8px; }
        .search-box input[type="text"] { flex: 1; padding: 10px 14px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #c9d1d9; font-size: 1em; }
        .search-box input[type="text"]:focus { border-color: #58a6ff; outline: none; }
        .search-box button, .crawl-form button { padding: 10px 20px; background: #238636; border: 1px solid #2ea043; border-radius: 6px; color: #fff; font-size: 1em; cursor: pointer; }
        .search-box button:hover, .crawl-form button:hover { background: #2ea043; }
        .stop-btn { padding: 8px 16px; background: #da3633; border: 1px solid #f85149; border-radius: 6px; color: #fff; font-size: 0.9em; cursor: pointer; margin-left: 12px; }
        .stop-btn:hover { background: #f85149; }
        .result { background: #161b22; border: 1px solid #30363d; border-radius: 6px; padding: 14px; margin-bottom: 8px; }
        .result .url { color: #58a6ff; font-size: 0.85em; word-break: break-all; }
        .result .title { font-weight: bold; margin: 4px 0; }
        .result .meta { color: #8b949e; font-size: 0.8em; }
        .status-badge { display: inline-block; padding: 2px 8px; border-radius: 10px; font-size: 0.75em; font-weight: bold; }
        .status-badge.running { background: #1f6feb33; color: #58a6ff; }
        .status-badge.complete { background: #23863633; color: #3fb950; }
        .status-badge.idle { background: #30363d; color: #8b949e; }
        .crawl-form { background: #161b22; border: 1px solid #30363d; border-radius: 6px; padding: 20px; margin-bottom: 24px; }
        .crawl-form h2 { color: #58a6ff; font-size: 1.1em; margin-bottom: 12px; }
        .crawl-form .field { margin-bottom: 12px; }
        .crawl-form .field label { display: block; color: #8b949e; font-size: 0.8em; margin-bottom: 4px; text-transform: uppercase; letter-spacing: 0.5px; }
        .crawl-form .field input[type="text"],
        .crawl-form .field input[type="number"] { width: 100%; padding: 8px 12px; background: #0d1117; border: 1px solid #30363d; border-radius: 6px; color: #c9d1d9; font-size: 1em; }
        .crawl-form .field input:focus { border-color: #58a6ff; outline: none; }
        .crawl-form .inline { display: flex; gap: 12px; }
        .crawl-form .inline .field { flex: 1; }
        .crawl-form .checkbox-field { display: flex; align-items: center; gap: 8px; margin-bottom: 12px; }
        .crawl-form .checkbox-field input { width: 16px; height: 16px; }
        .crawl-form .checkbox-field label { color: #c9d1d9; font-size: 0.9em; }
        .resume-banner { background: #1f6feb22; border: 1px solid #1f6feb; border-radius: 6px; padding: 14px 20px; margin-bottom: 20px; display: flex; align-items: center; justify-content: space-between; }
        .resume-banner .info { color: #58a6ff; font-size: 0.9em; }
        .resume-btn { padding: 8px 16px; background: #1f6feb; border: 1px solid #388bfd; border-radius: 6px; color: #fff; font-size: 0.9em; cursor: pointer; }
        .resume-btn:hover { background: #388bfd; }
    </style>
</head>
<body>
    <h1>Google in a Day
        {{if .IsRunning}}
            <span class="status-badge running">CRAWLING</span>
            <form method="POST" action="/" style="display:inline"><input type="hidden" name="action" value="stop"><button type="submit" class="stop-btn">Stop Crawl</button></form>
        {{else if .HasStarted}}
            <span class="status-badge complete">COMPLETE</span>
        {{else}}
            <span class="status-badge idle">IDLE</span>
        {{end}}
    </h1>
    <p class="subtitle">Live crawler dashboard{{if .IsRunning}} — auto-refreshes every 2 seconds{{end}}</p>

    {{if .CanResume}}
    <div class="resume-banner">
        <span class="info">Interrupted crawl found: <strong>{{.ResumeSeed}}</strong></span>
        <form method="POST" action="/" style="display:inline"><input type="hidden" name="action" value="resume"><button type="submit" class="resume-btn">Resume Crawl</button></form>
    </div>
    {{end}}

    {{if .ShowForm}}
    <div class="crawl-form">
        <h2>Start a Crawl</h2>
        <form method="POST" action="/">
            <div class="field">
                <label>Seed URL</label>
                <input type="text" name="seed" placeholder="https://example.com" required>
            </div>
            <div class="inline">
                <div class="field">
                    <label>Max Depth</label>
                    <input type="number" name="depth" value="3" min="1" max="20">
                </div>
                <div class="field">
                    <label>Workers</label>
                    <input type="number" name="workers" value="5" min="1" max="50">
                </div>
            </div>
            <div class="checkbox-field">
                <input type="checkbox" name="same_domain" id="same_domain" checked>
                <label for="same_domain">Same domain only</label>
            </div>
            <button type="submit">Start Crawl</button>
        </form>
    </div>
    {{end}}

    {{if .HasStarted}}
    <div class="grid">
        <div class="card">
            <div class="label">Pages Processed</div>
            <div class="value">{{.Metrics.PagesProcessed}}</div>
        </div>
        <div class="card">
            <div class="label">Pages Queued</div>
            <div class="value">{{.Metrics.PagesQueued}}</div>
        </div>
        <div class="card">
            <div class="label">Indexed Docs</div>
            <div class="value done">{{.Metrics.IndexedDocs}}</div>
        </div>
        <div class="card">
            <div class="label">Errors</div>
            <div class="value error">{{.Metrics.PagesErrored}}</div>
        </div>
        <div class="card">
            <div class="label">Queue Depth</div>
            <div class="value">{{.Metrics.QueueDepth}}</div>
        </div>
        <div class="card">
            <div class="label">Active Workers</div>
            <div class="value">{{.Metrics.ActiveWorkers}}</div>
        </div>
        <div class="card">
            <div class="label">Overflow Buffer</div>
            <div class="value {{if .Metrics.BackPressureActive}}error{{end}}">{{.Metrics.OverflowSize}}</div>
        </div>
        <div class="card">
            <div class="label">Uptime</div>
            <div class="value">{{.Metrics.UptimeStr}}</div>
        </div>
    </div>

    <div class="search-box">
        <form method="GET" action="/">
            <input type="text" name="q" placeholder="Search indexed pages..." value="{{.Query}}" autofocus>
            <button type="submit">Search</button>
        </form>
    </div>

    {{if .Query}}
        <p style="color: #8b949e; margin-bottom: 12px;">{{len .Results}} results for "{{.Query}}"</p>
        {{range .Results}}
        <div class="result">
            <div class="url">{{.URL}}</div>
            <div class="title">{{if .Title}}{{.Title}}{{else}}(no title){{end}}</div>
            <div class="meta">Score: {{printf "%.2f" .Score}} | Depth: {{.Depth}} | Origin: {{.OriginURL}}</div>
        </div>
        {{end}}
        {{if not .Results}}
            <p style="color: #8b949e;">No results found.</p>
        {{end}}
    {{end}}
    {{end}}
</body>
</html>`
