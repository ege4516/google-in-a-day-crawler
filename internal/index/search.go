package index

import (
	"sort"
)

// SearchResult represents a single ranked search hit.
type SearchResult struct {
	URL       string  `json:"url"`
	OriginURL string  `json:"origin_url"`
	Depth     int     `json:"depth"`
	Title     string  `json:"title"`
	Snippet   string  `json:"snippet"`
	Score     float64 `json:"score"`
}

// Search processes a query against the index and returns ranked results.
// Scoring: title match (3x) + URL match (2x) + body frequency (1x, capped).
func Search(query string, idx *Index, topK int) []SearchResult {
	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil
	}

	// Accumulate scores per URL
	type docInfo struct {
		url       string
		originURL string
		depth     int
		title     string
		score     float64
	}
	scores := make(map[string]*docInfo)

	for _, token := range queryTokens {
		postings := idx.Lookup(token)
		for _, p := range postings {
			info, ok := scores[p.URL]
			if !ok {
				info = &docInfo{
					url:       p.URL,
					originURL: p.OriginURL,
					depth:     p.Depth,
					title:     p.Title,
				}
				scores[p.URL] = info
			}

			// Title match: weight 3.0
			if p.InTitle {
				info.score += 3.0
			}

			// URL match: weight 2.0
			if p.InURL {
				info.score += 2.0
			}

			// Body frequency: weight 1.0, capped at 5
			tf := float64(p.TermFreq)
			if tf > 5 {
				tf = 5
			}
			info.score += tf / 5.0
		}
	}

	// Collect and sort
	results := make([]SearchResult, 0, len(scores))
	for _, info := range scores {
		results = append(results, SearchResult{
			URL:       info.url,
			OriginURL: info.originURL,
			Depth:     info.depth,
			Title:     info.title,
			Score:     info.score,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}

	return results
}
