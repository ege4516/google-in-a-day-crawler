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
	flag.StringVar(&cfg.SeedURL, "seed", "", "Seed URL (optional; omit for dashboard-only mode)")
	flag.IntVar(&cfg.MaxDepth, "depth", 3, "Maximum crawl depth from seed")
	flag.IntVar(&cfg.NumWorkers, "workers", 5, "Number of concurrent crawler workers")
	flag.IntVar(&cfg.QueueSize, "queue-size", 10000, "Bounded task queue capacity")
	flag.DurationVar(&cfg.RequestTimeout, "timeout", 10*time.Second, "HTTP request timeout")
	flag.Int64Var(&cfg.MaxBodySize, "max-body", 1<<20, "Maximum response body size in bytes")
	flag.BoolVar(&cfg.SameDomain, "same-domain", true, "Only crawl links on the seed domain")
	flag.IntVar(&cfg.DashboardPort, "port", 8080, "Dashboard HTTP port")
	flag.StringVar(&cfg.DataDir, "data", "data", "Directory for persistent storage")
	flag.Parse()
	return cfg
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

	// Check for resumable state
	if state, ok := manager.HasResumableState(); ok {
		fmt.Println("Google-in-a-Day Crawler")
		fmt.Printf("  Found interrupted crawl: %s (depth %d)\n", state.SeedURL, state.MaxDepth)
		fmt.Print("  Resume previous crawl? [Y/n]: ")

		var answer string
		fmt.Scanln(&answer)
		if answer == "" || answer == "y" || answer == "Y" || answer == "yes" {
			// Restore index from DB
			if err := manager.RestoreIndex(); err != nil {
				log.Printf("Warning: failed to restore index: %v", err)
			}

			resumeCfg := crawler.Config{
				SeedURL:        state.SeedURL,
				MaxDepth:       state.MaxDepth,
				NumWorkers:     state.NumWorkers,
				QueueSize:      cfg.QueueSize,
				RequestTimeout: cfg.RequestTimeout,
				MaxBodySize:    cfg.MaxBodySize,
				SameDomain:     state.SameDomain,
			}

			// Start dashboard in background
			dash := dashboard.NewServer(cfg.DashboardPort, manager)
			go func() {
				if err := dash.Start(); err != nil {
					log.Printf("Dashboard error: %v", err)
				}
			}()

			fmt.Printf("  Resuming crawl...\n")
			fmt.Printf("  Dashboard: http://localhost:%d\n\n", cfg.DashboardPort)

			done, err := manager.ResumeCrawl(resumeCfg)
			if err != nil {
				log.Fatalf("Failed to resume crawl: %v", err)
			}
			<-done

			fmt.Println()
			fmt.Println("Crawl finished. Dashboard still running for search.")
			fmt.Println("Press Ctrl+C to exit.")
			<-sigCh
			fmt.Println("Exiting.")
			return
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
		seedParsed, err := url.Parse(cfg.SeedURL)
		if err != nil || (seedParsed.Scheme != "http" && seedParsed.Scheme != "https") {
			fmt.Fprintf(os.Stderr, "Error: invalid seed URL %q (must be http or https)\n", cfg.SeedURL)
			os.Exit(1)
		}

		fmt.Println("Google-in-a-Day Crawler")
		fmt.Printf("  Seed:    %s\n", cfg.SeedURL)
		fmt.Printf("  Depth:   %d\n", cfg.MaxDepth)
		fmt.Printf("  Workers: %d\n", cfg.NumWorkers)
		fmt.Printf("  Queue:   %d\n", cfg.QueueSize)
		fmt.Printf("  Port:    %d\n", cfg.DashboardPort)
		fmt.Println()

		crawlerCfg := crawler.Config{
			SeedURL:        cfg.SeedURL,
			MaxDepth:       cfg.MaxDepth,
			NumWorkers:     cfg.NumWorkers,
			QueueSize:      cfg.QueueSize,
			RequestTimeout: cfg.RequestTimeout,
			MaxBodySize:    cfg.MaxBodySize,
			SameDomain:     cfg.SameDomain,
		}

		done, err := manager.StartCrawl(crawlerCfg)
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
