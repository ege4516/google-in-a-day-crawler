package crawler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// CrawlTask represents a single URL to be crawled.
type CrawlTask struct {
	URL       string
	OriginURL string
	Depth     int
}

// PageRecord holds the result of processing a single page.
type PageRecord struct {
	URL        string
	OriginURL  string
	Depth      int
	Title      string
	BodyText   string
	Links      []string
	StatusCode int
	CrawledAt  time.Time
	Error      string
}

// WorkerConfig holds parameters the worker needs.
type WorkerConfig struct {
	RequestTimeout time.Duration
	MaxBodySize    int64
	SameDomain     bool
	SeedHost       string
}

// workerLoop reads tasks from taskCh, processes each page, and sends results.
// It sends discovered URLs to discoveredCh and page content to resultsCh.
func workerLoop(ctx context.Context, id int, cfg WorkerConfig, taskCh <-chan CrawlTask, discoveredCh chan<- []CrawlTask, resultsCh chan<- PageRecord) {
	client := &http.Client{
		Timeout: cfg.RequestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	for task := range taskCh {
		if ctx.Err() != nil {
			// Send empty batch so coordinator can decrement inFlight
			discoveredCh <- nil
			continue
		}

		record := processPage(ctx, client, cfg, task)
		resultsCh <- record

		// Build new tasks from discovered links
		var newTasks []CrawlTask
		for _, link := range record.Links {
			newTasks = append(newTasks, CrawlTask{
				URL:       link,
				OriginURL: task.URL,
				Depth:     task.Depth + 1,
			})
		}
		discoveredCh <- newTasks
	}
}

// processPage fetches and parses a single page.
func processPage(ctx context.Context, client *http.Client, cfg WorkerConfig, task CrawlTask) PageRecord {
	record := PageRecord{
		URL:       task.URL,
		OriginURL: task.OriginURL,
		Depth:     task.Depth,
		CrawledAt: time.Now(),
	}

	body, statusCode, err := fetchPage(ctx, client, cfg, task.URL)
	record.StatusCode = statusCode
	if err != nil {
		record.Error = err.Error()
		return record
	}

	title, links, bodyText := parseHTML(body, task.URL)
	record.Title = title
	record.BodyText = bodyText

	// Normalize and filter links
	var validLinks []string
	for _, rawLink := range links {
		normalized, err := normalizeURL(rawLink, task.URL)
		if err != nil {
			continue
		}
		if !isValidCrawlTarget(normalized, cfg.SameDomain, cfg.SeedHost) {
			continue
		}
		validLinks = append(validLinks, normalized)
	}
	record.Links = validLinks

	return record
}

// fetchPage performs an HTTP GET with timeout and size limits.
func fetchPage(ctx context.Context, client *http.Client, cfg WorkerConfig, rawURL string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "GoogleInADay-Crawler/1.0 (educational project)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	// Only process HTML content
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") && !strings.Contains(ct, "application/xhtml") {
		return nil, resp.StatusCode, fmt.Errorf("non-HTML content-type: %s", ct)
	}

	// Limit body size
	limited := io.LimitReader(resp.Body, cfg.MaxBodySize)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}

	return body, resp.StatusCode, nil
}

// parseHTML extracts the title, all href links, and visible body text from raw HTML.
func parseHTML(body []byte, baseURL string) (title string, links []string, bodyText string) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return "", nil, ""
	}

	var textBuf strings.Builder
	var inTitle bool
	var titleBuf strings.Builder

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.ElementNode:
			switch n.Data {
			case "title":
				inTitle = true
			case "a":
				for _, attr := range n.Attr {
					if attr.Key == "href" {
						links = append(links, attr.Val)
					}
				}
			case "script", "style", "noscript":
				return // skip these subtrees entirely
			}
		case html.TextNode:
			if inTitle {
				titleBuf.WriteString(n.Data)
			}
			text := strings.TrimSpace(n.Data)
			if text != "" {
				textBuf.WriteString(text)
				textBuf.WriteString(" ")
			}
		}

		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}

		if n.Type == html.ElementNode && n.Data == "title" {
			inTitle = false
		}
	}
	walk(doc)

	title = strings.TrimSpace(titleBuf.String())
	bodyText = strings.TrimSpace(textBuf.String())
	return
}

// normalizeURL resolves a raw href against a base URL and canonicalizes it.
func normalizeURL(rawHref, baseURL string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(rawHref)
	if err != nil {
		return "", err
	}

	resolved := base.ResolveReference(ref)

	// Lowercase scheme and host
	resolved.Scheme = strings.ToLower(resolved.Scheme)
	resolved.Host = strings.ToLower(resolved.Host)

	// Strip fragment
	resolved.Fragment = ""

	// Strip trailing slash from path (unless it's just "/")
	if len(resolved.Path) > 1 {
		resolved.Path = strings.TrimRight(resolved.Path, "/")
	}

	return resolved.String(), nil
}

// isValidCrawlTarget checks if a URL should be crawled.
func isValidCrawlTarget(rawURL string, sameDomain bool, seedHost string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	// Only HTTP(S)
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}

	// Reject binary/non-HTML extensions
	lower := strings.ToLower(u.Path)
	skipExts := []string{
		".pdf", ".jpg", ".jpeg", ".png", ".gif", ".svg", ".bmp", ".ico", ".webp",
		".zip", ".tar", ".gz", ".rar", ".7z",
		".mp3", ".mp4", ".avi", ".mov", ".wmv", ".flv", ".webm",
		".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx",
		".css", ".js", ".json", ".xml", ".rss", ".atom",
		".exe", ".dmg", ".msi", ".deb", ".rpm",
		".woff", ".woff2", ".ttf", ".eot",
	}
	for _, ext := range skipExts {
		if strings.HasSuffix(lower, ext) {
			return false
		}
	}

	// Same-domain check
	if sameDomain && seedHost != "" {
		if strings.ToLower(u.Host) != strings.ToLower(seedHost) {
			return false
		}
	}

	return true
}
