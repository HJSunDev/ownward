package retrieval

import (
	"crypto/sha256"
	"io"
	"math"
)

func (s QueryTextScorer) digestTerms() map[[32]byte]int {
	terms := make(map[[32]byte]int, len(s.weights))
	for t, i := range s.words {
		terms[sha256.Sum256([]byte(t))] = i
	}
	for t, i := range s.singleCJK {
		terms[sha256.Sum256([]byte(string(t)))] = i
	}
	for t, i := range s.pairCJK {
		terms[sha256.Sum256([]byte(string(t[:])))] = i
	}
	return terms
}

// NewStreamPassageTextScorer retains only the query vocabulary and its passage
// frequencies. The source can contain any number of bounded passages.
func NewStreamPassageTextScorer(query string, walk func(func(string) error) error) (QueryTextScorer, error) {
	s := NewQueryTextScorer(query)
	terms := s.digestTerms()
	counts := make([]int, len(s.weights))
	passages := 0
	err := walk(func(text string) error {
		passages++
		seen := make(map[int]bool)
		for _, term := range tokenize(text) {
			if i, ok := terms[sha256.Sum256([]byte(term))]; ok && !seen[i] {
				counts[i]++
				seen[i] = true
			}
		}
		return nil
	})
	if err != nil {
		return s, err
	}
	if passages > 0 {
		for i := range s.weights {
			s.weights[i] *= math.Log1p(float64(passages) / float64(1+counts[i]))
		}
	}
	return s, nil
}

func (s QueryTextScorer) ScoreReader(r io.Reader) (float64, error) {
	terms := s.digestTerms()
	seen := make([]bool, len(s.weights))
	score := 0.0
	err := VisitTermDigests(r, func(d [32]byte) error {
		if i, ok := terms[d]; ok && !seen[i] {
			seen[i] = true
			score += s.weights[i]
		}
		return nil
	})
	return score, err
}
