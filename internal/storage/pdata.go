package storage

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// pdataMu protects concurrent writes to the p.data file.
var pdataMu sync.Mutex

// PDataPath returns the standard path for the postings flat file.
func PDataPath(dataDir string) string {
	return filepath.Join(dataDir, "storage", "p.data")
}

// AppendPostingsToFile appends posting rows to the p.data flat file.
// Format per line: word url origin depth frequency
// Thread-safe via file mutex.
func AppendPostingsToFile(path string, postings []PostingRow) error {
	if len(postings) == 0 {
		return nil
	}

	pdataMu.Lock()
	defer pdataMu.Unlock()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create storage dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open p.data: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	for _, p := range postings {
		origin := p.OriginURL
		if origin == "" {
			origin = "-"
		}
		fmt.Fprintf(w, "%s %s %s %d %d\n", p.Token, p.URL, origin, p.Depth, p.TermFreq)
	}
	return w.Flush()
}

// LoadPostingsFromFile reads all postings from the p.data flat file.
// Returns nil, nil if the file does not exist.
func LoadPostingsFromFile(path string) ([]PostingRow, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open p.data: %w", err)
	}
	defer f.Close()

	var result []PostingRow
	scanner := bufio.NewScanner(f)
	// Increase buffer for potentially long lines
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 5)
		if len(parts) < 5 {
			continue
		}

		depth, err := strconv.Atoi(parts[3])
		if err != nil {
			continue
		}
		freq, err := strconv.Atoi(parts[4])
		if err != nil {
			continue
		}

		origin := parts[2]
		if origin == "-" {
			origin = ""
		}

		result = append(result, PostingRow{
			Token:    parts[0],
			URL:      parts[1],
			OriginURL: origin,
			Depth:    depth,
			TermFreq: freq,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read p.data: %w", err)
	}

	return result, nil
}

// ClearPDataFile removes the p.data file (used before a fresh crawl).
func ClearPDataFile(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
