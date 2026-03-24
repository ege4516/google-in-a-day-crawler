package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Home</title></head><body>
			<a href="/a">Page A</a>
			<a href="/b">Page B</a>
			<p>Welcome to the test site.</p>
		</body></html>`)
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Page A</title></head><body>
			<a href="/c">Page C</a>
			<p>Content of page A about programming.</p>
		</body></html>`)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Page B</title></head><body>
			<p>Content of page B about testing.</p>
		</body></html>`)
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Page C</title></head><body>
			<p>Content of page C about search engines.</p>
		</body></html>`)
	})
	return httptest.NewServer(mux)
}

func TestManager_StartCrawl(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(ctx, nil, "")

	cfg := Config{
		SeedURL:        ts.URL,
		MaxDepth:       2,
		NumWorkers:     2,
		QueueSize:      100,
		RequestTimeout: 5 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}

	_, done, err := m.StartCrawl(cfg)
	if err != nil {
		t.Fatalf("StartCrawl failed: %v", err)
	}
	<-done

	idx := m.GetIndex()
	if idx.DocCount() < 3 {
		t.Errorf("expected at least 3 indexed docs, got %d", idx.DocCount())
	}

	if m.IsRunning() {
		t.Error("expected IsRunning=false after crawl completes")
	}
}

func TestManager_ConcurrentCrawls(t *testing.T) {
	// Two separate test servers to crawl concurrently
	ts1 := newTestServer()
	defer ts1.Close()
	ts2 := newTestServer()
	defer ts2.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(ctx, nil, "")

	cfg1 := Config{
		SeedURL:        ts1.URL,
		MaxDepth:       2,
		NumWorkers:     2,
		QueueSize:      100,
		RequestTimeout: 5 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}
	cfg2 := Config{
		SeedURL:        ts2.URL,
		MaxDepth:       2,
		NumWorkers:     2,
		QueueSize:      100,
		RequestTimeout: 5 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}

	id1, done1, err := m.StartCrawl(cfg1)
	if err != nil {
		t.Fatalf("first StartCrawl failed: %v", err)
	}

	id2, done2, err := m.StartCrawl(cfg2)
	if err != nil {
		t.Fatalf("second StartCrawl failed: %v", err)
	}

	// Both should have unique IDs
	if id1 == id2 {
		t.Error("expected different session IDs for concurrent crawls")
	}

	// Both should be running
	if m.RunningCount() < 1 {
		t.Error("expected at least 1 running crawl after starting two")
	}

	<-done1
	<-done2

	if m.IsRunning() {
		t.Error("expected IsRunning=false after both crawls complete")
	}

	// Index should have accumulated docs from both crawls
	idx := m.GetIndex()
	if idx.DocCount() < 3 {
		t.Errorf("expected at least 3 indexed docs from concurrent crawls, got %d", idx.DocCount())
	}
}

func TestManager_StopCrawl(t *testing.T) {
	// Slow server to ensure crawl is still running when we stop it
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>Slow page</body></html>`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(ctx, nil, "")

	cfg := Config{
		SeedURL:        ts.URL,
		MaxDepth:       1,
		NumWorkers:     1,
		QueueSize:      100,
		RequestTimeout: 10 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}

	_, done, err := m.StartCrawl(cfg)
	if err != nil {
		t.Fatalf("StartCrawl failed: %v", err)
	}

	// Give it a moment to start, then stop
	time.Sleep(100 * time.Millisecond)
	m.StopCrawl()

	select {
	case <-done:
		// Good — crawl stopped
	case <-time.After(3 * time.Second):
		t.Error("crawl did not stop within 3 seconds after StopCrawl")
	}
}

func TestManager_StopCrawlByID(t *testing.T) {
	// Two slow servers
	makeSlow := func() *httptest.Server {
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(5 * time.Second):
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body>Slow</body></html>`)
		})
		return httptest.NewServer(mux)
	}
	ts1 := makeSlow()
	defer ts1.Close()
	ts2 := makeSlow()
	defer ts2.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(ctx, nil, "")

	cfg1 := Config{
		SeedURL:        ts1.URL,
		MaxDepth:       1,
		NumWorkers:     1,
		QueueSize:      100,
		RequestTimeout: 10 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}
	cfg2 := Config{
		SeedURL:        ts2.URL,
		MaxDepth:       1,
		NumWorkers:     1,
		QueueSize:      100,
		RequestTimeout: 10 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}

	id1, done1, err := m.StartCrawl(cfg1)
	if err != nil {
		t.Fatalf("first StartCrawl failed: %v", err)
	}
	_, _, err = m.StartCrawl(cfg2)
	if err != nil {
		t.Fatalf("second StartCrawl failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Stop only the first crawl
	ok := m.StopCrawlByID(id1)
	if !ok {
		t.Error("StopCrawlByID returned false for running crawl")
	}

	select {
	case <-done1:
		// Good — first crawl stopped
	case <-time.After(3 * time.Second):
		t.Error("first crawl did not stop within 3 seconds after StopCrawlByID")
	}

	// Second crawl should still be running
	if !m.IsRunning() {
		t.Error("expected IsRunning=true while second crawl is still running")
	}

	// Clean up: stop all
	m.StopCrawl()
}

func TestManager_IsRunning(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(ctx, nil, "")

	// Initially not running
	if m.IsRunning() {
		t.Error("expected IsRunning=false before any crawl")
	}

	cfg := Config{
		SeedURL:        ts.URL,
		MaxDepth:       1,
		NumWorkers:     2,
		QueueSize:      100,
		RequestTimeout: 5 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}

	_, done, _ := m.StartCrawl(cfg)

	// Should be running briefly
	if !m.IsRunning() {
		// Might have finished already for a fast test server; that's acceptable
		t.Log("crawl finished before IsRunning check (fast server)")
	}

	<-done

	// After done, should not be running
	if m.IsRunning() {
		t.Error("expected IsRunning=false after crawl completes")
	}
}

func TestManager_IndexAccumulates(t *testing.T) {
	ts := newTestServer()
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(ctx, nil, "")

	cfg := Config{
		SeedURL:        ts.URL,
		MaxDepth:       0, // only seed page
		NumWorkers:     1,
		QueueSize:      100,
		RequestTimeout: 5 * time.Second,
		MaxBodySize:    1 << 20,
		SameDomain:     true,
	}

	// First crawl: depth 0 = only seed page
	_, done1, err := m.StartCrawl(cfg)
	if err != nil {
		t.Fatalf("first crawl failed: %v", err)
	}
	<-done1
	countAfterFirst := m.GetIndex().DocCount()

	// Second crawl: depth 1 = seed + linked pages
	cfg.MaxDepth = 1
	_, done2, err := m.StartCrawl(cfg)
	if err != nil {
		t.Fatalf("second crawl failed: %v", err)
	}
	<-done2
	countAfterSecond := m.GetIndex().DocCount()

	if countAfterSecond <= countAfterFirst {
		t.Errorf("expected index to grow after second crawl: first=%d, second=%d",
			countAfterFirst, countAfterSecond)
	}
}
