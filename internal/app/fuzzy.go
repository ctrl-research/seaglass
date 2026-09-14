package app

import (
	"strings"

	"github.com/sahilm/fuzzy"
)

// fuzzyFilter matches query against haystack. Every whitespace-separated
// term must fuzzy match; the score is the sum over terms. It returns the
// matching indices in haystack order plus their scores, or all indices with
// nil scores when the query is blank. Matching is case-insensitive when the
// haystack is already lowercase.
func fuzzyFilter(query string, haystack []string) ([]int, map[int]int) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		all := make([]int, len(haystack))
		for i := range haystack {
			all[i] = i
		}
		return all, nil
	}
	var scores map[int]int
	for ti, term := range strings.Fields(q) {
		found := map[int]int{}
		for _, m := range fuzzy.Find(term, haystack) {
			found[m.Index] = m.Score
		}
		if ti == 0 {
			scores = found
			continue
		}
		for idx := range scores {
			if s, ok := found[idx]; ok {
				scores[idx] += s
			} else {
				delete(scores, idx)
			}
		}
	}
	idx := make([]int, 0, len(scores))
	for i := range haystack {
		if _, ok := scores[i]; ok {
			idx = append(idx, i)
		}
	}
	return idx, scores
}
