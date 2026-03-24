package index

import (
	"testing"
)

func setupTestIndex() *Index {
	idx := NewIndex()
	idx.AddDocument(Document{
		URL:      "https://example.com/go",
		Title:    "Go Programming Language",
		BodyText: "Go is an open source programming language that makes it easy to build software",
	})
	idx.AddDocument(Document{
		URL:      "https://example.com/rust",
		Title:    "Rust Language",
		BodyText: "Rust is a language empowering everyone to build reliable and efficient software programming systems",
	})
	idx.AddDocument(Document{
		URL:      "https://example.com/python",
		Title:    "Python",
		BodyText: "Python is a popular programming language used for web development and data science",
	})
	return idx
}

func TestSearch_EmptyQuery(t *testing.T) {
	idx := setupTestIndex()
	results := Search("", idx, 10, "relevance")
	if len(results) != 0 {
		t.Errorf("empty query should return 0 results, got %d", len(results))
	}
}

func TestSearch_StopWordsOnly(t *testing.T) {
	idx := setupTestIndex()
	results := Search("the and is", idx, 10, "relevance")
	if len(results) != 0 {
		t.Errorf("query of only stop words should return 0 results, got %d", len(results))
	}
}

func TestSearch_TitleMatchScoresHigher(t *testing.T) {
	idx := setupTestIndex()
	results := Search("rust", idx, 10, "relevance")
	if len(results) == 0 {
		t.Fatal("expected results for 'rust'")
	}
	// "Rust Language" has "rust" in title → should score highest
	if results[0].URL != "https://example.com/rust" {
		t.Errorf("expected rust page first, got %q (score %.2f)", results[0].URL, results[0].Score)
	}
}

func TestSearch_ReturnsResults(t *testing.T) {
	idx := setupTestIndex()
	results := Search("programming", idx, 10, "relevance")
	if len(results) < 2 {
		t.Fatalf("expected >= 2 results for 'programming', got %d", len(results))
	}
}

func TestSearch_TopKLimit(t *testing.T) {
	idx := setupTestIndex()
	results := Search("programming", idx, 1, "relevance")
	if len(results) > 1 {
		t.Errorf("topK=1 but got %d results", len(results))
	}
}

func TestSearch_MultiToken(t *testing.T) {
	idx := setupTestIndex()
	results := Search("go programming", idx, 10, "relevance")
	if len(results) == 0 {
		t.Fatal("expected results for multi-token query")
	}
	// The go page has both tokens → should score highest
	if results[0].URL != "https://example.com/go" {
		t.Errorf("expected go page first for 'go programming', got %q", results[0].URL)
	}
}

func TestSearch_ResultFields(t *testing.T) {
	idx := NewIndex()
	idx.AddDocument(Document{
		URL:       "https://example.com/test",
		OriginURL: "https://example.com",
		Depth:     2,
		Title:     "Test Page",
		BodyText:  "testing content",
	})
	results := Search("test", idx, 10, "relevance")
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	r := results[0]
	if r.URL != "https://example.com/test" {
		t.Errorf("URL = %q", r.URL)
	}
	if r.OriginURL != "https://example.com" {
		t.Errorf("OriginURL = %q", r.OriginURL)
	}
	if r.Depth != 2 {
		t.Errorf("Depth = %d", r.Depth)
	}
	if r.Title != "Test Page" {
		t.Errorf("Title = %q", r.Title)
	}
	if r.Score <= 0 {
		t.Errorf("Score should be > 0, got %.2f", r.Score)
	}
}

func TestSearch_NoMatch(t *testing.T) {
	idx := setupTestIndex()
	results := Search("xyznonexistent", idx, 10, "relevance")
	if len(results) != 0 {
		t.Errorf("expected 0 results for non-matching query, got %d", len(results))
	}
}
