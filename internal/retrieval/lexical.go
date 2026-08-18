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
	docs          map[string]uint32
	documents     []*indexedDocument
	terms         map[string]uint32
	termEntries   []termEntry
	totalTerm     int
	postingCount  int
	liveTermCount int
	generation    uint32
}

type indexedDocument struct {
	information domain.Information
	length      int
	generation  uint32
}

type posting struct {
	document   uint32
	frequency  uint32
	generation uint32
}

type termEntry struct {
	documentFrequency int
	postings          []posting
}

func NewLexical(values []domain.Information) *Lexical {
	index := &Lexical{
		docs:        make(map[string]uint32, len(values)),
		documents:   make([]*indexedDocument, 0, len(values)),
		terms:       make(map[string]uint32),
		termEntries: make([]termEntry, 0),
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
		id  uint32
		idf float64
	}
	uniqueTerms := make(map[string]struct{})
	maxDocumentFrequency := len(l.documents) / 4
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
		termID, exists := l.terms[term]
		if !exists {
			continue
		}
		documentFrequency := l.termEntries[termID].documentFrequency
		if documentFrequency == 0 || documentFrequency > maxDocumentFrequency {
			continue
		}
		idf := math.Log(1+(float64(len(l.documents)-documentFrequency)+0.5)/(float64(documentFrequency)+0.5)) * queryTermWeight(term)
		queryTerms = append(queryTerms, weightedTerm{id: termID, idf: idf})
		candidateCapacity += documentFrequency
	}
	if candidateCapacity > len(l.documents) {
		candidateCapacity = len(l.documents)
	}
	trimmedQuery := strings.TrimSpace(query)
	averageLength := 1.0
	if len(l.documents) > 0 && l.totalTerm > 0 {
		averageLength = float64(l.totalTerm) / float64(len(l.documents))
	}
	scores := make(map[uint32]float64, candidateCapacity)
	for _, term := range queryTerms {
		for _, posting := range l.termEntries[term.id].postings {
			if int(posting.document) >= len(l.documents) {
				continue
			}
			document := l.documents[posting.document]
			if document == nil || document.generation != posting.generation || l.docs[document.information.ID] != posting.document || !matchesContexts(document.information.Contexts, contexts) {
				continue
			}
			frequency := float64(posting.frequency)
			length := float64(document.length)
			const k1, b = 1.2, 0.75
			scores[posting.document] += term.idf * (frequency * (k1 + 1)) / (frequency + k1*(1-b+b*length/averageLength))
		}
	}
	if documentID, exists := l.docs[trimmedQuery]; exists {
		document := l.documents[documentID]
		if document != nil && matchesContexts(document.information.Contexts, contexts) {
			scores[documentID] += 1000
		}
	}
	best := make(resultHeap, 0, limit)
	for documentID, score := range scores {
		document := l.documents[documentID]
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
	if l.generation == ^uint32(0) {
		for _, existing := range l.documents {
			if existing != nil {
				existing.generation = 1
			}
		}
		l.generation = 1
		l.rebuildPostingsLocked()
	}
	documentID, exists := l.docs[value.ID]
	if exists {
		previous := l.documents[documentID]
		_, previousFrequencies := lexicalTerms(previous.information)
		l.totalTerm -= previous.length
		l.liveTermCount -= len(previousFrequencies)
		for term := range previousFrequencies {
			if termID, found := l.terms[term]; found {
				l.termEntries[termID].documentFrequency--
			}
		}
	} else {
		documentID = uint32(len(l.documents))
		l.docs[value.ID] = documentID
		l.documents = append(l.documents, nil)
	}
	tokens, frequencies := lexicalTerms(value)
	l.generation++
	value.Contexts = append([]domain.Context(nil), value.Contexts...)
	value.Relations = append([]domain.ExplicitRelation(nil), value.Relations...)
	document := &indexedDocument{information: value, length: len(tokens), generation: l.generation}
	for term, frequency := range frequencies {
		termID, exists := l.terms[term]
		if !exists {
			termID = uint32(len(l.termEntries))
			l.terms[term] = termID
			l.termEntries = append(l.termEntries, termEntry{})
		}
		entry := &l.termEntries[termID]
		entry.documentFrequency++
		entry.postings = append(entry.postings, posting{document: documentID, frequency: frequency, generation: document.generation})
		l.postingCount++
	}
	l.documents[documentID] = document
	l.totalTerm += len(tokens)
	l.liveTermCount += len(frequencies)
	if l.postingCount > l.liveTermCount*2+1000 {
		l.rebuildPostingsLocked()
	}
}

func (l *Lexical) rebuildPostingsLocked() {
	for index := range l.termEntries {
		l.termEntries[index].postings = nil
	}
	count := 0
	for documentID, document := range l.documents {
		if document == nil {
			continue
		}
		_, frequencies := lexicalTerms(document.information)
		for term, frequency := range frequencies {
			termID, exists := l.terms[term]
			if !exists {
				continue
			}
			entry := &l.termEntries[termID]
			entry.postings = append(entry.postings, posting{document: uint32(documentID), frequency: frequency, generation: document.generation})
			count++
		}
	}
	l.postingCount = count
}

func lexicalTerms(value domain.Information) ([]string, map[string]uint32) {
	parts := []string{value.ID, string(value.Kind), value.Content}
	for _, context := range value.Contexts {
		parts = append(parts, context.Key, context.Value)
	}
	for _, relation := range value.Relations {
		parts = append(parts, relation.Type, relation.TargetID)
	}
	tokens := tokenize(strings.Join(parts, " "))
	frequencies := make(map[string]uint32, len(tokens))
	for _, token := range tokens {
		frequencies[token]++
	}
	return tokens, frequencies
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
