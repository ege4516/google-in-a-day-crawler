package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ege/google-in-a-day/internal/index"
)

// buildTestServer creates an httptest server with a known link structure:
//
//	/ -> links to /a, /b
//	/a -> links to /c
//	/b -> links to /c (duplicate)
//	/c -> no outbound links
func buildTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Home</title></head><body>
			<p>Welcome home page</p>
			<a href="/a">Page A</a>
			<a href="/b">Page B</a>
		</body></html>`)
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Page A</title></head><body>
			<p>This is page alpha content</p>
			<a href="/c">Page C</a>
		</body></html>`)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Page B</title></head><body>
			<p>This is page beta content</p>
			<a href="/c">Page C</a>
		</body></html>`)
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Page C</title></head><body>
			<p>This is page charlie with no outbound links</p>
		</body></html>`)
	})
	return httptest.NewServer(mux)
}

func TestCrawler_AllPagesReached(t *testing.T) {
	ts := buildTestServer()
	defer ts.Close()

	idx := index.NewIndex()
	metrics := &Metrics{}

	cfg := Config{
		SeedURL:        ts.URL + "/",
		MaxDepth:       3,
		NumWorkers:     3,
		QueueSize:      100,
		RequestTimeout: 5 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}

	c := NewCrawler(cfg, 0, idx, metrics, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c.Start(ctx, nil)

	// Should have indexed all 4 pages: /, /a, /b, /c
	docCount := idx.DocCount()
	if docCount != 4 {
		t.Errorf("expected 4 indexed docs, got %d", docCount)
	}

	processed := metrics.PagesProcessed.Load()
	if processed != 4 {
		t.Errorf("PagesProcessed = %d, want 4", processed)
	}

	if !metrics.CrawlDone.Load() {
		t.Error("CrawlDone should be true after Start() returns")
	}
}

func TestCrawler_DepthLimit(t *testing.T) {
	ts := buildTestServer()
	defer ts.Close()

	idx := index.NewIndex()
	metrics := &Metrics{}

	cfg := Config{
		SeedURL:        ts.URL + "/",
		MaxDepth:       1, // Only / and its direct links (/a, /b)
		NumWorkers:     2,
		QueueSize:      100,
		RequestTimeout: 5 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}

	c := NewCrawler(cfg, 0, idx, metrics, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c.Start(ctx, nil)

	// Depth 0: /, Depth 1: /a, /b. /c is depth 2 → should not be reached.
	docCount := idx.DocCount()
	if docCount != 3 {
		t.Errorf("expected 3 indexed docs at depth 1, got %d", docCount)
	}
}

func TestCrawler_NoDuplicates(t *testing.T) {
	ts := buildTestServer()
	defer ts.Close()

	idx := index.NewIndex()
	metrics := &Metrics{}

	cfg := Config{
		SeedURL:        ts.URL + "/",
		MaxDepth:       3,
		NumWorkers:     3,
		QueueSize:      100,
		RequestTimeout: 5 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}

	c := NewCrawler(cfg, 0, idx, metrics, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c.Start(ctx, nil)

	// /c is linked from both /a and /b but should be crawled exactly once.
	// Total unique pages: 4
	processed := metrics.PagesProcessed.Load()
	if processed != 4 {
		t.Errorf("PagesProcessed = %d, want exactly 4 (no duplicates)", processed)
	}
}

func TestCrawler_SearchDuringCrawl(t *testing.T) {
	// Build a server with many pages to give us time to search during crawl
	mux := http.NewServeMux()
	for i := 0; i < 20; i++ {
		page := fmt.Sprintf("/page%d", i)
		nextPage := fmt.Sprintf("/page%d", i+1)
		mux.HandleFunc(page, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			// Add a small delay to simulate network latency
			time.Sleep(10 * time.Millisecond)
			fmt.Fprintf(w, `<html><head><title>Page</title></head><body>
				<p>searchable content here</p>
				<a href="%s">Next</a>
			</body></html>`, nextPage)
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Home</title></head><body>
			<p>searchable homepage</p>
			<a href="/page0">Start</a>
		</body></html>`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	idx := index.NewIndex()
	metrics := &Metrics{}

	cfg := Config{
		SeedURL:        ts.URL + "/",
		MaxDepth:       5,
		NumWorkers:     2,
		QueueSize:      100,
		RequestTimeout: 5 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}

	c := NewCrawler(cfg, 0, idx, metrics, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Run crawler in background
	done := make(chan struct{})
	go func() {
		c.Start(ctx, nil)
		close(done)
	}()

	// Wait a bit then search while crawling
	time.Sleep(200 * time.Millisecond)
	results := index.Search("searchable", idx, 10)
	// We should find at least the homepage (it's fast to crawl)
	if len(results) == 0 && idx.DocCount() > 0 {
		t.Log("Warning: search returned 0 results but docs exist — timing dependent")
	}

	// Search should not panic or deadlock — that's the main assertion
	<-done
}

func TestCrawler_ContextCancellation(t *testing.T) {
	// Server that's slow to respond
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><a href="/slow">Slow</a></body></html>`)
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second) // very slow
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>Slow page</body></html>`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	idx := index.NewIndex()
	metrics := &Metrics{}

	cfg := Config{
		SeedURL:        ts.URL + "/",
		MaxDepth:       2,
		NumWorkers:     2,
		QueueSize:      100,
		RequestTimeout: 5 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}

	c := NewCrawler(cfg, 0, idx, metrics, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	start := time.Now()
	c.Start(ctx, nil)
	elapsed := time.Since(start)

	// Should complete within ~2 seconds (1s timeout + some leeway), not 10+
	if elapsed > 5*time.Second {
		t.Errorf("crawler took %v to shut down, expected < 5s", elapsed)
	}
}

func TestCrawler_MetricsConsistency(t *testing.T) {
	ts := buildTestServer()
	defer ts.Close()

	idx := index.NewIndex()
	metrics := &Metrics{}

	cfg := Config{
		SeedURL:        ts.URL + "/",
		MaxDepth:       3,
		NumWorkers:     3,
		QueueSize:      100,
		RequestTimeout: 5 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}

	c := NewCrawler(cfg, 0, idx, metrics, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c.Start(ctx, nil)

	snap := metrics.Snapshot()

	// PagesProcessed = IndexedDocs + PagesErrored
	if snap.PagesProcessed != snap.IndexedDocs+snap.PagesErrored {
		t.Errorf("Processed(%d) != Indexed(%d) + Errors(%d)",
			snap.PagesProcessed, snap.IndexedDocs, snap.PagesErrored)
	}

	// ActiveWorkers should be 0 after completion
	if snap.ActiveWorkers != 0 {
		t.Errorf("ActiveWorkers = %d after completion, want 0", snap.ActiveWorkers)
	}

	// No errors expected for our test server
	if snap.PagesErrored != 0 {
		t.Errorf("PagesErrored = %d, want 0", snap.PagesErrored)
	}

	// Uptime should be > 0 since we set StartTime
	if snap.Uptime <= 0 {
		t.Error("Uptime should be positive")
	}
}
