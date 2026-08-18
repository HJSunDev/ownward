package retrieval

import (
	"container/heap"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/HJSunDev/ownward/internal/domain"
)

type Result struct {
	Information domain.Information `json:"information"`
	Score       float64            `json:"score"`
	Signals     []string           `json:"signals"`
}

type Lexical struct {
	mu            sync.RWMutex
	docs          map[string]*indexedDocument
	postings      map[string][]posting
	docFreq       map[string]int
	totalTerm     int
	postingCount  int
	liveTermCount int
	generation    uint64
}

type indexedDocument struct {
	information domain.Information
	terms       []string
	length      int
	generation  uint64
}

type posting struct {
	document   *indexedDocument
	frequency  int
	generation uint64
}

func NewLexical(values []domain.Information) *Lexical {
	index := &Lexical{
		docs:     make(map[string]*indexedDocument, len(values)),
		postings: make(map[string][]posting),
		docFreq:  make(map[string]int),
	}
	for _, value := range values {
		index.upsertLocked(value)
	}
	return index
}

func (l *Lexical) Upsert(value domain.Information) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.upsertLocked(value)
}

func (l *Lexical) Search(query string, contexts []domain.Context, limit int) []Result {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if limit <= 0 {
		limit = 10
	}
	type weightedTerm struct {
		value string
		idf   float64
	}
	uniqueTerms := make(map[string]struct{})
	maxDocumentFrequency := len(l.docs) / 4
	if maxDocumentFrequency < 16 {
		maxDocumentFrequency = 16
	}
	queryTerms := make([]weightedTerm, 0, 16)
	candidateCapacity := 1
	for _, term := range tokenize(query) {
		if _, duplicate := uniqueTerms[term]; duplicate {
			continue
		}
		uniqueTerms[term] = struct{}{}
		documentFrequency := l.docFreq[term]
		if documentFrequency == 0 || documentFrequency > maxDocumentFrequency {
			continue
		}
		idf := math.Log(1 + (float64(len(l.docs)-documentFrequency)+0.5)/(float64(documentFrequency)+0.5))
		queryTerms = append(queryTerms, weightedTerm{value: term, idf: idf})
		candidateCapacity += documentFrequency
	}
	if candidateCapacity > len(l.docs) {
		candidateCapacity = len(l.docs)
	}
	trimmedQuery := strings.TrimSpace(query)
	averageLength := 1.0
	if len(l.docs) > 0 && l.totalTerm > 0 {
		averageLength = float64(l.totalTerm) / float64(len(l.docs))
	}
	scores := make(map[*indexedDocument]float64, candidateCapacity)
	for _, term := range queryTerms {
		for _, posting := range l.postings[term.value] {
			document := posting.document
			if document.generation != posting.generation || l.docs[document.information.ID] != document || !matchesContexts(document.information.Contexts, contexts) {
				continue
			}
			frequency := float64(posting.frequency)
			length := float64(document.length)
			const k1, b = 1.2, 0.75
			scores[document] += term.idf * (frequency * (k1 + 1)) / (frequency + k1*(1-b+b*length/averageLength))
		}
	}
	if document := l.docs[trimmedQuery]; document != nil && matchesContexts(document.information.Contexts, contexts) {
		scores[document] += 1000
	}
	best := make(resultHeap, 0, limit)
	for document, score := range scores {
		identity := strings.EqualFold(trimmedQuery, document.information.ID)
		if score > 0 {
			signals := make([]string, 0, 2)
			if identity {
				signals = append(signals, "identity")
			}
			if !identity || score > 1000 {
				signals = append(signals, "lexical")
			}
			result := Result{Information: document.information, Score: score, Signals: signals}
			if len(best) < limit {
				heap.Push(&best, result)
			} else if betterResult(result, best[0]) {
				best[0] = result
				heap.Fix(&best, 0)
			}
		}
	}
	results := append([]Result(nil), best...)
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].Information.ID < results[j].Information.ID
		}
		return results[i].Score > results[j].Score
	})
	return results
}

type resultHeap []Result

func (h resultHeap) Len() int { return len(h) }
func (h resultHeap) Less(left, right int) bool {
	if h[left].Score == h[right].Score {
		return h[left].Information.ID > h[right].Information.ID
	}
	return h[left].Score < h[right].Score
}
func (h resultHeap) Swap(left, right int) { h[left], h[right] = h[right], h[left] }
func (h *resultHeap) Push(value any)      { *h = append(*h, value.(Result)) }
func (h *resultHeap) Pop() any {
	previous := *h
	last := previous[len(previous)-1]
	*h = previous[:len(previous)-1]
	return last
}

func betterResult(left, right Result) bool {
	if left.Score == right.Score {
		return left.Information.ID < right.Information.ID
	}
	return left.Score > right.Score
}

func (l *Lexical) upsertLocked(value domain.Information) {
	if previous := l.docs[value.ID]; previous != nil {
		l.totalTerm -= previous.length
		l.liveTermCount -= len(previous.terms)
		for _, term := range previous.terms {
			l.docFreq[term]--
			if l.docFreq[term] == 0 {
				delete(l.docFreq, term)
			}
		}
	}
	parts := []string{value.ID, string(value.Kind), value.Content}
	for _, context := range value.Contexts {
		parts = append(parts, context.Key, context.Value)
	}
	for _, relation := range value.Relations {
		parts = append(parts, relation.Type, relation.TargetID)
	}
	tokens := tokenize(strings.Join(parts, " "))
	frequencies := make(map[string]int, len(tokens))
	for _, token := range tokens {
		frequencies[token]++
	}
	l.generation++
	value.Contexts = append([]domain.Context(nil), value.Contexts...)
	value.Relations = append([]domain.ExplicitRelation(nil), value.Relations...)
	document := &indexedDocument{information: value, terms: make([]string, 0, len(frequencies)), length: len(tokens), generation: l.generation}
	for term, frequency := range frequencies {
		document.terms = append(document.terms, term)
		l.docFreq[term]++
		l.postings[term] = append(l.postings[term], posting{document: document, frequency: frequency, generation: document.generation})
		l.postingCount++
	}
	l.docs[value.ID] = document
	l.totalTerm += len(tokens)
	l.liveTermCount += len(document.terms)
	if l.postingCount > l.liveTermCount*2+1000 {
		l.rebuildPostingsLocked()
	}
}

func (l *Lexical) rebuildPostingsLocked() {
	postings := make(map[string][]posting, len(l.postings))
	count := 0
	for _, document := range l.docs {
		frequencies := make(map[string]int, len(document.terms))
		parts := []string{document.information.ID, string(document.information.Kind), document.information.Content}
		for _, context := range document.information.Contexts {
			parts = append(parts, context.Key, context.Value)
		}
		for _, relation := range document.information.Relations {
			parts = append(parts, relation.Type, relation.TargetID)
		}
		for _, token := range tokenize(strings.Join(parts, " ")) {
			frequencies[token]++
		}
		for term, frequency := range frequencies {
			postings[term] = append(postings[term], posting{document: document, frequency: frequency, generation: document.generation})
			count++
		}
	}
	l.postings = postings
	l.postingCount = count
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
		case unicode.Is(unicode.Han, current), unicode.Is(unicode.Hiragana, current), unicode.Is(unicode.Katakana, current), unicode.Is(unicode.Hangul, current):
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

func matchesContexts(actual, required []domain.Context) bool {
	if len(required) == 0 {
		return true
	}
	// 没有场景限定的信息适用于所有场景；只有同名场景键给出冲突值时才排除。
	for _, expected := range required {
		declared := false
		compatible := false
		for _, candidate := range actual {
			if strings.EqualFold(candidate.Key, expected.Key) {
				declared = true
				if strings.EqualFold(candidate.Value, expected.Value) {
					compatible = true
				}
			}
		}
		if declared && !compatible {
			return false
		}
	}
	return true
}
