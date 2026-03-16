package index

import (
	"sync"
	"testing"
	"time"
)

func TestAddDocument_And_Lookup(t *testing.T) {
	idx := NewIndex()
	idx.AddDocument(Document{
		URL:      "https://example.com/go",
		Title:    "Go Programming Language",
		BodyText: "Go is an open source programming language",
	})

	postings := idx.Lookup("programming")
	if len(postings) != 1 {
		t.Fatalf("expected 1 posting for 'programming', got %d", len(postings))
	}
	if postings[0].URL != "https://example.com/go" {
		t.Errorf("posting URL = %q", postings[0].URL)
	}
	if !postings[0].InTitle {
		t.Error("'programming' should be marked InTitle")
	}
}

func TestAddDocument_TermFrequency(t *testing.T) {
	idx := NewIndex()
	idx.AddDocument(Document{
		URL:      "https://example.com",
		Title:    "Test",
		BodyText: "rust rust rust python rust",
	})

	postings := idx.Lookup("rust")
	if len(postings) != 1 {
		t.Fatalf("expected 1 posting for 'rust', got %d", len(postings))
	}
	// Body has 4 "rust" occurrences
	if postings[0].TermFreq < 4 {
		t.Errorf("TermFreq = %d, want >= 4", postings[0].TermFreq)
	}
}

func TestLookup_UnknownToken(t *testing.T) {
	idx := NewIndex()
	postings := idx.Lookup("nonexistent")
	if len(postings) != 0 {
		t.Errorf("expected 0 postings for unknown token, got %d", len(postings))
	}
}

func TestDocCount(t *testing.T) {
	idx := NewIndex()
	if idx.DocCount() != 0 {
		t.Errorf("empty index should have 0 docs")
	}
	idx.AddDocument(Document{URL: "https://a.com", Title: "A", BodyText: "alpha"})
	idx.AddDocument(Document{URL: "https://b.com", Title: "B", BodyText: "beta"})
	if idx.DocCount() != 2 {
		t.Errorf("DocCount = %d, want 2", idx.DocCount())
	}
}

func TestTokenize_Lowercases(t *testing.T) {
	tokens := tokenize("Hello WORLD GoLang")
	for _, tok := range tokens {
		for _, r := range tok {
			if r >= 'A' && r <= 'Z' {
				t.Errorf("token %q has uppercase", tok)
			}
		}
	}
}

func TestTokenize_RemovesShortWords(t *testing.T) {
	tokens := tokenize("I am a go developer")
	for _, tok := range tokens {
		if len(tok) < 2 {
			t.Errorf("token %q is too short (< 2 chars)", tok)
		}
	}
}

func TestTokenize_RemovesStopWords(t *testing.T) {
	tokens := tokenize("the quick brown fox is not here")
	tokenSet := make(map[string]bool)
	for _, t := range tokens {
		tokenSet[t] = true
	}
	for _, sw := range []string{"the", "is", "not"} {
		if tokenSet[sw] {
			t.Errorf("stop word %q should be removed", sw)
		}
	}
}

func TestTokenize_SplitsOnNonAlphaNum(t *testing.T) {
	tokens := tokenize("hello-world foo_bar baz.qux")
	tokenSet := make(map[string]bool)
	for _, t := range tokens {
		tokenSet[t] = true
	}
	if !tokenSet["hello"] {
		t.Error("expected 'hello'")
	}
	if !tokenSet["world"] {
		t.Error("expected 'world'")
	}
	if !tokenSet["foo"] {
		t.Error("expected 'foo'")
	}
	if !tokenSet["bar"] {
		t.Error("expected 'bar'")
	}
}

func TestAddDocument_URLField(t *testing.T) {
	idx := NewIndex()
	idx.AddDocument(Document{
		URL:       "https://example.com/page",
		OriginURL: "https://example.com",
		Depth:     1,
		Title:     "Test Page",
		BodyText:  "content here",
	})
	postings := idx.Lookup("page")
	if len(postings) == 0 {
		t.Fatal("expected postings for 'page'")
	}
	if !postings[0].InURL {
		t.Error("'page' appears in URL, should have InURL=true")
	}
	if postings[0].OriginURL != "https://example.com" {
		t.Errorf("OriginURL = %q", postings[0].OriginURL)
	}
}

// ---------- Concurrency test ----------

func TestIndex_ConcurrentReadWrite(t *testing.T) {
	idx := NewIndex()
	done := make(chan struct{})

	// Writer goroutine: adds documents continuously
	go func() {
		for i := 0; i < 200; i++ {
			idx.AddDocument(Document{
				URL:      "https://example.com/" + string(rune('a'+i%26)),
				Title:    "Document concurrent test",
				BodyText: "concurrent write read safety test document body",
			})
		}
		close(done)
	}()

	// Reader goroutines: search continuously
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deadline := time.After(2 * time.Second)
			for {
				select {
				case <-deadline:
					return
				default:
					_ = idx.Lookup("concurrent")
					_ = idx.DocCount()
				}
			}
		}()
	}

	<-done
	wg.Wait()
	// If we get here without panic or race, the test passes
}
