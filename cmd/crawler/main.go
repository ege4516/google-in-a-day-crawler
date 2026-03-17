package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ege/google-in-a-day/internal/crawler"
	"github.com/ege/google-in-a-day/internal/dashboard"
	"github.com/ege/google-in-a-day/internal/storage"
)

// CrawlConfig holds all tunable parameters parsed from CLI flags.
type CrawlConfig struct {
	SeedURL        string
	MaxDepth       int
	MaxURLs        int
	NumWorkers     int
	QueueSize      int
	RequestTimeout time.Duration
	MaxBodySize    int64
	SameDomain     bool
	DashboardPort  int
	DataDir        string
}

func parseFlags() CrawlConfig {
	cfg := CrawlConfig{}
	flag.StringVar(&cfg.SeedURL, "seed", "", "Seed URL(s), comma-separated for multiple (optional; omit for dashboard-only mode)")
	flag.IntVar(&cfg.MaxDepth, "depth", 3, "Maximum crawl depth from seed")
	flag.IntVar(&cfg.MaxURLs, "max-urls", 0, "Maximum total URLs to visit (0 = unlimited)")
	flag.IntVar(&cfg.NumWorkers, "workers", 5, "Number of concurrent crawler workers")
	flag.IntVar(&cfg.QueueSize, "queue-size", 10000, "Bounded task queue capacity")
	flag.DurationVar(&cfg.RequestTimeout, "timeout", 10*time.Second, "HTTP request timeout")
	flag.Int64Var(&cfg.MaxBodySize, "max-body", 1<<20, "Maximum response body size in bytes")
	flag.BoolVar(&cfg.SameDomain, "same-domain", true, "Only crawl links on the seed domain(s)")
	flag.IntVar(&cfg.DashboardPort, "port", 8080, "Dashboard HTTP port")
	flag.StringVar(&cfg.DataDir, "data", "data", "Directory for persistent storage")
	flag.Parse()
	return cfg
}

// parseSeeds splits comma-separated URLs and validates each one.
func parseSeeds(raw string) ([]string, error) {
	var seeds []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("invalid seed URL %q (must be http or https)", s)
		}
		seeds = append(seeds, s)
	}
	if len(seeds) == 0 {
		return nil, fmt.Errorf("no valid seed URLs provided")
	}
	return seeds, nil
}

func main() {
	cfg := parseFlags()

	// Context with signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received %v, shutting down gracefully...", sig)
		cancel()
	}()

	// Open persistent storage
	dbPath := filepath.Join(cfg.DataDir, "crawler.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		log.Printf("Warning: could not open database at %s: %v (running without persistence)", dbPath, err)
		db = nil
	} else {
		defer db.Close()
	}

	// Create manager (owns index + metrics lifecycle)
	manager := crawler.NewManager(ctx, db)

	// Restore index from persisted data
	if db != nil {
		if err := manager.RestoreIndex(); err != nil {
			log.Printf("Warning: failed to restore index: %v", err)
		}
	}

	// Start dashboard in background
	dash := dashboard.NewServer(cfg.DashboardPort, manager)
	go func() {
		if err := dash.Start(); err != nil {
			log.Printf("Dashboard error: %v", err)
		}
	}()

	if cfg.SeedURL != "" {
		// Mode A: CLI-initiated crawl
		seeds, err := parseSeeds(cfg.SeedURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("Google-in-a-Day Crawler")
		if len(seeds) == 1 {
			fmt.Printf("  Seed:    %s\n", seeds[0])
		} else {
			fmt.Printf("  Seeds:   %d URLs\n", len(seeds))
			for i, s := range seeds {
				fmt.Printf("    [%d] %s\n", i+1, s)
			}
		}
		fmt.Printf("  Depth:   %d\n", cfg.MaxDepth)
		fmt.Printf("  Workers: %d\n", cfg.NumWorkers)
		fmt.Printf("  Queue:   %d\n", cfg.QueueSize)
		fmt.Printf("  Port:    %d\n", cfg.DashboardPort)
		fmt.Println()

		crawlerCfg := crawler.Config{
			SeedURL:        seeds[0],
			SeedURLs:       seeds,
			MaxDepth:       cfg.MaxDepth,
			MaxURLs:        cfg.MaxURLs,
			NumWorkers:     cfg.NumWorkers,
			QueueSize:      cfg.QueueSize,
			RequestTimeout: cfg.RequestTimeout,
			MaxBodySize:    cfg.MaxBodySize,
			SameDomain:     cfg.SameDomain,
		}

		_, done, err := manager.StartCrawl(crawlerCfg)
		if err != nil {
			log.Fatalf("Failed to start crawl: %v", err)
		}
		<-done

		fmt.Println()
		fmt.Println("Crawl finished. Dashboard still running for search.")
		fmt.Println("Press Ctrl+C to exit.")
	} else {
		// Mode B: Dashboard-only mode
		fmt.Println("Google-in-a-Day Crawler — Dashboard Mode")
		fmt.Printf("  Open http://localhost:%d to start a crawl\n", cfg.DashboardPort)
		fmt.Println("  Press Ctrl+C to exit.")
	}

	// Wait for signal to fully exit
	<-sigCh
	fmt.Println("Exiting.")
}
