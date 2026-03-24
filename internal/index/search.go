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
	Frequency int     `json:"frequency"`
}

// Search processes a query against the index and returns ranked results.
// Scoring: score = (frequency × 10) + 1000 (exact match bonus) − (depth × 5)
// sortBy: "relevance" (default), "depth", "frequency"
func Search(query string, idx *Index, topK int, sortBy string) []SearchResult {
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
		frequency int
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

			// Relevance score: (frequency × 10) + 1000 (exact match bonus) − (depth × 5)
			info.score += float64(p.TermFreq*10) + 1000.0 - float64(p.Depth*5)
			info.frequency += p.TermFreq
		}
	}

	// Collect results
	results := make([]SearchResult, 0, len(scores))
	for _, info := range scores {
		results = append(results, SearchResult{
			URL:       info.url,
			OriginURL: info.originURL,
			Depth:     info.depth,
			Title:     info.title,
			Score:     info.score,
			Frequency: info.frequency,
		})
	}

	// Sort based on sortBy parameter
	switch sortBy {
	case "depth":
		sort.Slice(results, func(i, j int) bool {
			if results[i].Depth != results[j].Depth {
				return results[i].Depth < results[j].Depth
			}
			return results[i].Score > results[j].Score // tie-break by score
		})
	case "frequency":
		sort.Slice(results, func(i, j int) bool {
			if results[i].Frequency != results[j].Frequency {
				return results[i].Frequency > results[j].Frequency
			}
			return results[i].Score > results[j].Score // tie-break by score
		})
	default: // "relevance"
		sort.Slice(results, func(i, j int) bool {
			return results[i].Score > results[j].Score
		})
	}

	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}

	return results
}
