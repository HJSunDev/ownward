package retrieval

import (
	"math"
	"strings"
	"unicode"
)

func queryTermWeight(term string) float64 {
	runes := []rune(term)
	if len(runes) == 1 {
		current := runes[0]
		if unicode.Is(unicode.Han, current) || unicode.Is(unicode.Hiragana, current) || unicode.Is(unicode.Katakana, current) || unicode.Is(unicode.Hangul, current) {
			return 0.25
		}
		return 1
	}
	for _, current := range runes {
		if unicode.Is(unicode.Han, current) || unicode.Is(unicode.Hiragana, current) || unicode.Is(unicode.Katakana, current) || unicode.Is(unicode.Hangul, current) {
			return 1
		}
	}
	return 1.5
}

// QueryTextScorer compiles a query once for late passage selection. It keeps
// no source text or corpus index.
type QueryTextScorer struct {
	singleCJK map[rune]int
	pairCJK   map[[2]rune]int
	words     map[string]int
	weights   []float64
}

func NewQueryTextScorer(query string) QueryTextScorer {
	unique := make(map[string]struct{})
	scorer := QueryTextScorer{
		singleCJK: make(map[rune]int), pairCJK: make(map[[2]rune]int), words: make(map[string]int),
	}
	for _, term := range tokenize(query) {
		if _, exists := unique[term]; exists {
			continue
		}
		unique[term] = struct{}{}
		index := len(scorer.weights)
		scorer.weights = append(scorer.weights, queryTermWeight(term))
		runes := []rune(term)
		switch {
		case len(runes) == 1 && isCJK(runes[0]):
			scorer.singleCJK[runes[0]] = index
		case len(runes) == 2 && isCJK(runes[0]) && isCJK(runes[1]):
			scorer.pairCJK[[2]rune{runes[0], runes[1]}] = index
		default:
			scorer.words[term] = index
		}
	}
	return scorer
}

// NewPassageTextScorer discounts terms repeated across one source's passages.
// It does not change asset ranking or retain a corpus-wide index.
func NewPassageTextScorer(query string, passages []string) QueryTextScorer {
	scorer := NewQueryTextScorer(query)
	if len(passages) == 0 {
		return scorer
	}
	frequencies := make(map[string]int)
	for _, passage := range passages {
		seen := make(map[string]bool)
		for _, term := range tokenize(passage) {
			if !seen[term] {
				frequencies[term]++
				seen[term] = true
			}
		}
	}
	for term, index := range scorer.words {
		scorer.weights[index] *= math.Log1p(float64(len(passages)) / float64(1+frequencies[term]))
	}
	for term, index := range scorer.singleCJK {
		scorer.weights[index] *= math.Log1p(float64(len(passages)) / float64(1+frequencies[string(term)]))
	}
	for term, index := range scorer.pairCJK {
		scorer.weights[index] *= math.Log1p(float64(len(passages)) / float64(1+frequencies[string(term[:])]))
	}
	return scorer
}

// Score scores real passage text; each query term contributes at most once so
// a concise, specific match outranks boilerplate frequency.
func (s QueryTextScorer) Score(text string) float64 {
	score := 0.0
	var seen uint64
	var seenLarge []uint64
	if len(s.weights) > 64 {
		seenLarge = make([]uint64, (len(s.weights)+63)/64)
	}
	mark := func(index int) {
		if len(seenLarge) == 0 {
			bit := uint64(1) << index
			if seen&bit == 0 {
				seen |= bit
				score += s.weights[index]
			}
			return
		}
		word, bit := index/64, uint64(1)<<uint(index%64)
		if seenLarge[word]&bit == 0 {
			seenLarge[word] |= bit
			score += s.weights[index]
		}
	}
	word := make([]rune, 0, 16)
	flushWord := func() {
		if len(word) == 0 {
			return
		}
		if index, exists := s.words[string(word)]; exists {
			mark(index)
		}
		word = word[:0]
	}
	var previousCJK rune
	previousWasCJK := false
	for _, current := range text {
		switch {
		case isCJK(current):
			flushWord()
			if index, exists := s.singleCJK[current]; exists {
				mark(index)
			}
			if previousWasCJK {
				if index, exists := s.pairCJK[[2]rune{previousCJK, current}]; exists {
					mark(index)
				}
			}
			previousCJK, previousWasCJK = current, true
		case unicode.IsLetter(current) || unicode.IsDigit(current):
			previousWasCJK = false
			word = append(word, unicode.ToLower(current))
		default:
			flushWord()
			previousWasCJK = false
		}
	}
	flushWord()
	return score
}

// QueryTextScore is the convenience form for one-off callers.
func QueryTextScore(query, text string) float64 {
	return NewQueryTextScorer(query).Score(text)
}

func tokenize(value string) []string {
	value = strings.ToLower(value)
	tokens := make([]string, 0, len(value)/4)
	var word []rune
	var cjk []rune
	flushWord := func() {
		if len(word) > 0 {
			tokens = append(tokens, string(word))
			word = word[:0]
		}
	}
	flushCJK := func() {
		if len(cjk) == 0 {
			return
		}
		for _, current := range cjk {
			tokens = append(tokens, string(current))
		}
		for index := 0; index+1 < len(cjk); index++ {
			tokens = append(tokens, string(cjk[index:index+2]))
		}
		cjk = cjk[:0]
	}
	for _, current := range value {
		switch {
		case isCJK(current):
			flushWord()
			cjk = append(cjk, current)
		case unicode.IsLetter(current) || unicode.IsDigit(current):
			flushCJK()
			word = append(word, current)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return tokens
}

func isCJK(current rune) bool {
	return unicode.Is(unicode.Han, current) || unicode.Is(unicode.Hiragana, current) || unicode.Is(unicode.Katakana, current) || unicode.Is(unicode.Hangul, current)
}
