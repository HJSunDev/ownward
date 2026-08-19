package semantics

import (
	"context"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/domain"
)

const DefaultEmbeddingDimensions = 384

type Cue struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
}

type Relation struct {
	Type           string  `json:"type"`
	TargetID       string  `json:"target_id"`
	TargetRevision uint64  `json:"target_revision,omitempty"`
	Confidence     float64 `json:"confidence"`
	Evidence       string  `json:"evidence,omitempty"`
	Direction      string  `json:"direction,omitempty"`
	InferredBy     string  `json:"inferred_by,omitempty"`
}

type Analysis struct {
	Kind      domain.InformationKind `json:"kind"`
	Summary   string                 `json:"summary"`
	Cues      []Cue                  `json:"cues"`
	Topics    []string               `json:"topics"`
	Contexts  []domain.Context       `json:"inferred_contexts,omitempty"`
	Relations []Relation             `json:"relations,omitempty"`
}

type Candidate struct {
	ID         string                 `json:"id"`
	Kind       domain.InformationKind `json:"kind"`
	Content    string                 `json:"content"`
	Contexts   []domain.Context       `json:"contexts,omitempty"`
	Similarity float64                `json:"semantic_similarity,omitempty"`
	Relations  []Relation             `json:"existing_relations,omitempty"`
}

type Provider interface {
	Analyze(context.Context, domain.Information, []Candidate) (Analysis, error)
	Embed(context.Context, []string) ([][]float32, error)
	Name() string
}

type Heuristic struct{}

func (Heuristic) Name() string {
	return "builtin-heuristic-v1"
}

func (Heuristic) Analyze(_ context.Context, value domain.Information, _ []Candidate) (Analysis, error) {
	tokens := significantTokens(value.Content)
	if len(tokens) > 12 {
		tokens = tokens[:12]
	}
	cues := make([]Cue, 0, len(tokens)+len(value.Contexts))
	for _, token := range tokens {
		cues = append(cues, Cue{Text: token, Kind: "term"})
	}
	for _, item := range value.Contexts {
		cues = append(cues, Cue{Text: item.Key + ":" + item.Value, Kind: "context"})
	}
	relations := make([]Relation, 0, len(value.Relations))
	for _, relation := range value.Relations {
		relations = append(relations, Relation{Type: relation.Type, TargetID: relation.TargetID, Confidence: 1})
	}
	return Analysis{
		Kind:      value.Kind,
		Summary:   truncate(value.Content, 240),
		Cues:      cues,
		Topics:    append([]string(nil), tokens...),
		Contexts:  append([]domain.Context(nil), value.Contexts...),
		Relations: relations,
	}, nil
}

func (Heuristic) Embed(_ context.Context, values []string) ([][]float32, error) {
	result := make([][]float32, len(values))
	for index, value := range values {
		vector := make([]float32, DefaultEmbeddingDimensions)
		tokens := significantTokens(value)
		for _, token := range tokens {
			hasher := fnv.New64a()
			_, _ = hasher.Write([]byte(token))
			hash := hasher.Sum64()
			position := int(hash % DefaultEmbeddingDimensions)
			sign := float32(1)
			if hash&(1<<63) != 0 {
				sign = -1
			}
			vector[position] += sign
		}
		normalize(vector)
		result[index] = vector
	}
	return result, nil
}

func significantTokens(value string) []string {
	counts := make(map[string]int)
	var latin []rune
	var han []rune
	flushLatin := func() {
		if len(latin) >= 2 {
			counts[string(latin)]++
		}
		latin = latin[:0]
	}
	flushHan := func() {
		for index := 0; index+1 < len(han); index++ {
			counts[string(han[index:index+2])]++
		}
		han = han[:0]
	}
	for _, current := range strings.ToLower(value) {
		switch {
		case unicode.Is(unicode.Han, current):
			flushLatin()
			han = append(han, current)
		case unicode.IsLetter(current) || unicode.IsDigit(current):
			flushHan()
			latin = append(latin, current)
		default:
			flushLatin()
			flushHan()
		}
	}
	flushLatin()
	flushHan()
	type pair struct {
		value string
		count int
	}
	pairs := make([]pair, 0, len(counts))
	for value, count := range counts {
		pairs = append(pairs, pair{value: value, count: count})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count == pairs[j].count {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].count > pairs[j].count
	})
	result := make([]string, 0, len(pairs))
	for _, item := range pairs {
		result = append(result, item.value)
	}
	return result
}

func normalize(vector []float32) {
	length := float64(0)
	for _, value := range vector {
		length += float64(value * value)
	}
	if length == 0 {
		return
	}
	divisor := float32(math.Sqrt(length))
	for index := range vector {
		vector[index] /= divisor
	}
}

func truncate(value string, length int) string {
	if utf8.RuneCountInString(value) <= length {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:length])) + "…"
}

func normalizeAnalysis(source domain.Information, value Analysis) Analysis {
	if strings.TrimSpace(value.Summary) == "" {
		value.Summary = truncate(source.Content, 240)
	} else {
		value.Summary = truncate(strings.TrimSpace(value.Summary), 512)
	}
	value.Cues = normalizeCues(value.Cues, 24)
	value.Topics = normalizeStrings(value.Topics, 24, 128)
	value.Contexts = normalizeInferredContexts(source.Contexts, value.Contexts, 16)
	value.Relations = normalizeRelations(value.Relations, 32)
	return value
}

func normalizeCues(values []Cue, limit int) []Cue {
	result := make([]Cue, 0, minInt(len(values), limit))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value.Text = truncate(strings.TrimSpace(value.Text), 128)
		value.Kind = truncate(strings.TrimSpace(value.Kind), 64)
		if value.Text == "" || value.Kind == "" {
			continue
		}
		key := strings.ToLower(value.Kind) + "\x00" + strings.ToLower(value.Text)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func normalizeStrings(values []string, limit, length int) []string {
	result := make([]string, 0, minInt(len(values), limit))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = truncate(strings.TrimSpace(value), length)
		key := strings.ToLower(value)
		if value == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func normalizeInferredContexts(explicit, inferred []domain.Context, limit int) []domain.Context {
	explicitByKey := make(map[string]map[string]struct{}, len(explicit))
	for _, value := range explicit {
		key := strings.ToLower(strings.TrimSpace(value.Key))
		if explicitByKey[key] == nil {
			explicitByKey[key] = make(map[string]struct{})
		}
		explicitByKey[key][strings.ToLower(strings.TrimSpace(value.Value))] = struct{}{}
	}
	result := make([]domain.Context, 0, minInt(len(inferred), limit))
	seen := make(map[string]struct{}, len(inferred))
	for _, value := range inferred {
		value.Key = truncate(strings.TrimSpace(value.Key), 128)
		value.Value = truncate(strings.TrimSpace(value.Value), 256)
		if value.Key == "" || value.Value == "" {
			continue
		}
		key := strings.ToLower(value.Key)
		item := strings.ToLower(value.Value)
		if allowed, constrained := explicitByKey[key]; constrained {
			if _, matches := allowed[item]; !matches {
				continue
			}
		}
		identity := key + "\x00" + item
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func normalizeRelations(values []Relation, limit int) []Relation {
	result := make([]Relation, 0, minInt(len(values), limit))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value.Type = strings.TrimSpace(value.Type)
		value.TargetID = strings.TrimSpace(value.TargetID)
		value.Evidence = truncate(strings.TrimSpace(value.Evidence), 240)
		key := value.Direction + "\x00" + value.Type + "\x00" + value.TargetID
		if value.Type == "" || value.TargetID == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
