package index

import (
	"strings"
	"sync"
	"unicode"
)

// Posting represents one (token, document) pair in the inverted index.
type Posting struct {
	URL       string
	OriginURL string
	Depth     int
	Title     string
	TermFreq  int
	InTitle   bool
	InURL     bool
}

// Document holds the data needed to index a single page.
type Document struct {
	URL       string
	OriginURL string
	Depth     int
	Title     string
	BodyText  string
}

// Index is a thread-safe inverted index.
type Index struct {
	mu       sync.RWMutex
	postings map[string][]Posting // token -> postings
	docCount int
}

// NewIndex creates an empty inverted index.
func NewIndex() *Index {
	return &Index{
		postings: make(map[string][]Posting),
	}
}

// AddDocument tokenizes and indexes a document. Thread-safe (acquires write lock).
func (idx *Index) AddDocument(doc Document) {
	titleTokens := tokenize(doc.Title)
	bodyTokens := tokenize(doc.BodyText)
	urlTokens := tokenize(doc.URL)

	titleSet := toSet(titleTokens)
	urlSet := toSet(urlTokens)

	// Count term frequency in body
	freq := make(map[string]int)
	for _, t := range bodyTokens {
		freq[t]++
	}
	// Also count title tokens
	for _, t := range titleTokens {
		freq[t]++
	}

	// Collect all unique tokens
	allTokens := make(map[string]struct{})
	for _, t := range bodyTokens {
		allTokens[t] = struct{}{}
	}
	for _, t := range titleTokens {
		allTokens[t] = struct{}{}
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()

	for token := range allTokens {
		p := Posting{
			URL:       doc.URL,
			OriginURL: doc.OriginURL,
			Depth:     doc.Depth,
			Title:     doc.Title,
			TermFreq:  freq[token],
			InTitle:   titleSet[token],
			InURL:     urlSet[token],
		}
		idx.postings[token] = append(idx.postings[token], p)
	}
	idx.docCount++
}

// Lookup returns all postings for a token. Thread-safe (acquires read lock).
func (idx *Index) Lookup(token string) []Posting {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	result := make([]Posting, len(idx.postings[token]))
	copy(result, idx.postings[token])
	return result
}

// DocCount returns the number of indexed documents.
func (idx *Index) DocCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.docCount
}

// AddPosting adds a single pre-built posting directly to the index.
// Used for restoring from persistent storage. Thread-safe.
func (idx *Index) AddPosting(token string, p Posting) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.postings[token] = append(idx.postings[token], p)
}

// SetDocCount sets the document count directly. Used when restoring from storage.
func (idx *Index) SetDocCount(n int) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.docCount = n
}

// Tokenize splits text into lowercase tokens, removing stop words and non-alpha chars.
func tokenize(text string) []string {
	lower := strings.ToLower(text)
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	var tokens []string
	for _, w := range words {
		if len(w) < 2 {
			continue
		}
		if stopWords[w] {
			continue
		}
		tokens = append(tokens, w)
	}
	return tokens
}

func toSet(tokens []string) map[string]bool {
	s := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		s[t] = true
	}
	return s
}

var stopWords = map[string]bool{
	"the": true, "be": true, "to": true, "of": true, "and": true,
	"in": true, "that": true, "have": true, "it": true, "for": true,
	"not": true, "on": true, "with": true, "he": true, "as": true,
	"you": true, "do": true, "at": true, "this": true, "but": true,
	"his": true, "by": true, "from": true, "they": true, "we": true,
	"say": true, "her": true, "she": true, "or": true, "an": true,
	"will": true, "my": true, "one": true, "all": true, "would": true,
	"there": true, "their": true, "what": true, "so": true, "up": true,
	"out": true, "if": true, "about": true, "who": true, "get": true,
	"which": true, "go": true, "me": true, "when": true, "make": true,
	"can": true, "like": true, "no": true, "just": true, "him": true,
	"know": true, "take": true, "into": true, "your": true, "some": true,
	"could": true, "them": true, "see": true, "other": true, "than": true,
	"then": true, "now": true, "its": true, "also": true, "after": true,
	"use": true, "how": true, "our": true, "was": true, "is": true,
	"are": true, "has": true, "had": true, "were": true, "been": true,
	"am": true, "did": true, "does": true, "a": true,
}
