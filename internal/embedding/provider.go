package embedding

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"unicode"
)

type Space struct {
	ID         string `json:"id"`
	Dimensions int    `json:"dimensions"`
}

type Provider interface {
	Name() string
	Space() Space
	EmbedDocuments(context.Context, []string) ([][]float32, error)
	EmbedQuery(context.Context, string) ([]float32, error)
	Close() error
}

type Unavailable struct {
	Reason string
}

func (u Unavailable) Name() string { return "unavailable" }
func (u Unavailable) Space() Space { return Space{} }
func (u Unavailable) EmbedDocuments(context.Context, []string) ([][]float32, error) {
	return nil, u.err()
}
func (u Unavailable) EmbedQuery(context.Context, string) ([]float32, error) { return nil, u.err() }
func (Unavailable) Close() error                                            { return nil }
func (u Unavailable) err() error {
	reason := strings.TrimSpace(u.Reason)
	if reason == "" {
		reason = "本地向量能力不可用"
	}
	return errors.New(reason)
}

// HashForTesting 是不进入正式产品路径的确定性测试向量能力。
type HashForTesting struct {
	Dimensions int
}

func (h HashForTesting) Name() string { return "test-hash-embedding" }
func (h HashForTesting) Space() Space {
	dimensions := h.Dimensions
	if dimensions <= 0 {
		dimensions = 384
	}
	return Space{ID: "test-hash-embedding", Dimensions: dimensions}
}
func (h HashForTesting) EmbedDocuments(_ context.Context, values []string) ([][]float32, error) {
	result := make([][]float32, len(values))
	for index, value := range values {
		result[index] = h.embed(value)
	}
	return result, nil
}
func (h HashForTesting) EmbedQuery(_ context.Context, value string) ([]float32, error) {
	return h.embed(value), nil
}
func (HashForTesting) Close() error { return nil }

func (h HashForTesting) embed(value string) []float32 {
	space := h.Space()
	vector := make([]float32, space.Dimensions)
	for _, token := range significantTokens(value) {
		hasher := fnv.New64a()
		_, _ = hasher.Write([]byte(token))
		hash := hasher.Sum64()
		position := int(hash % uint64(space.Dimensions))
		sign := float32(1)
		if hash&(1<<63) != 0 {
			sign = -1
		}
		vector[position] += sign
	}
	normalize(vector)
	return vector
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
	sort.Slice(pairs, func(left, right int) bool {
		if pairs[left].count == pairs[right].count {
			return pairs[left].value < pairs[right].value
		}
		return pairs[left].count > pairs[right].count
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
