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

var allowedRelationTypes = [...]string{
	"same_as",
	"broader_than",
	"narrower_than",
	"part_of",
	"has_part",
	"supports",
	"contradicts",
	"derived_from",
	"applies_in",
	"related_to",
}

var allowedRelationDirections = [...]string{"outgoing", "incoming"}

func AllowedRelationTypes() []string {
	return append([]string(nil), allowedRelationTypes[:]...)
}

func AllowedRelationDirections() []string {
	return append([]string(nil), allowedRelationDirections[:]...)
}

func IsAllowedRelationType(value string) bool {
	for _, allowed := range allowedRelationTypes {
		if value == allowed {
			return true
		}
	}
	return false
}

func IsAllowedRelationDirection(value string) bool {
	for _, allowed := range allowedRelationDirections {
		if value == allowed {
			return true
		}
	}
	return false
}

type Cue struct {
	Text string `json:"text" jsonschema:"有助于未来检索的原文线索"`
	Kind string `json:"kind" jsonschema:"线索类别，例如 entity、term 或 requirement"`
}

type Relation struct {
	Type           string  `json:"type" jsonschema:"关系类型必须取最精确的一种：same_as 表示含义等价；broader_than/narrower_than 只表示类别或概念范围的上位/下位，不能代替组成关系；part_of/has_part 表示机制、结构、流程、体系或主题中的组成/包含；supports 表示源资产为目标资产提供证据、机制、条件、方法或解决方案，问题、原因或背景不能反向支持解决它的方法；contradicts 表示明确冲突；derived_from 表示源结论、选择或做法由目标依据推导，不能用双向 supports 代替；applies_in 表示源内容适用于目标场景；只有存在直接关系但以上类型均不准确时才用 related_to。候选支持当前资产时仍填写 supports 并使用 incoming，不能自创逆向类型"`
	TargetID       string  `json:"target_id" jsonschema:"必须是当前语义工作提供的候选资产 id"`
	TargetRevision uint64  `json:"target_revision,omitempty"`
	Confidence     float64 `json:"confidence" jsonschema:"基于明确证据的置信度，关系至少为 0.75"`
	Evidence       string  `json:"evidence,omitempty" jsonschema:"当前资产与候选内容中直接支持该关系的证据"`
	Direction      string  `json:"direction,omitempty" jsonschema:"方向决定关系陈述的主语：outgoing 表示 当前资产 type 候选资产；incoming 表示 候选资产 type 当前资产。例如候选是支持当前结论的证据时，type 为 supports 且 direction 为 incoming"`
	InferredBy     string  `json:"inferred_by,omitempty"`
}

type InferredContext struct {
	Key        string  `json:"key" jsonschema:"只有信息含义或适用性确实依赖场景时才提供的场景键"`
	Value      string  `json:"value" jsonschema:"场景值"`
	Confidence float64 `json:"confidence" jsonschema:"场景判断置信度"`
	Evidence   string  `json:"evidence" jsonschema:"资产内容中直接支持该场景的证据"`
}

type Analysis struct {
	Summary   string            `json:"summary" jsonschema:"不改变原意的简洁语义摘要"`
	Cues      []Cue             `json:"cues" jsonschema:"有助于未来检索的线索；没有则为空数组"`
	Topics    []string          `json:"topics" jsonschema:"可多重归属的主题；没有可靠主题则为空数组"`
	Contexts  []InferredContext `json:"inferred_contexts,omitempty"`
	Relations []Relation        `json:"relations,omitempty"`
}

type Candidate struct {
	ID         string           `json:"id"`
	Revision   uint64           `json:"revision"`
	Content    string           `json:"content"`
	Contexts   []domain.Context `json:"explicit_contexts,omitempty"`
	Similarity float64          `json:"semantic_similarity,omitempty"`
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
		Summary:   truncate(value.Content, 240),
		Cues:      cues,
		Topics:    append([]string(nil), tokens...),
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

func NormalizeAnalysis(source domain.Information, value Analysis) Analysis {
	return normalizeAnalysis(source, value)
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

func normalizeInferredContexts(explicit []domain.Context, inferred []InferredContext, limit int) []InferredContext {
	explicitByKey := make(map[string]map[string]struct{}, len(explicit))
	for _, value := range explicit {
		key := strings.ToLower(strings.TrimSpace(value.Key))
		if explicitByKey[key] == nil {
			explicitByKey[key] = make(map[string]struct{})
		}
		explicitByKey[key][strings.ToLower(strings.TrimSpace(value.Value))] = struct{}{}
	}
	result := make([]InferredContext, 0, minInt(len(inferred), limit))
	seen := make(map[string]struct{}, len(inferred))
	for _, value := range inferred {
		value.Key = truncate(strings.TrimSpace(value.Key), 128)
		value.Value = truncate(strings.TrimSpace(value.Value), 256)
		value.Evidence = truncate(strings.TrimSpace(value.Evidence), 240)
		if value.Key == "" || value.Value == "" || value.Evidence == "" || value.Confidence < 0.75 || value.Confidence > 1 ||
			math.IsNaN(value.Confidence) || math.IsInf(value.Confidence, 0) {
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
	positions := make(map[string]int, len(values))
	for _, value := range values {
		value.Type = strings.TrimSpace(value.Type)
		value.TargetID = strings.TrimSpace(value.TargetID)
		value.Evidence = truncate(strings.TrimSpace(value.Evidence), 240)
		key := value.Direction + "\x00" + value.TargetID
		if value.Type == "" || value.TargetID == "" || value.Evidence == "" || value.Confidence < 0.75 || value.Confidence > 1 || math.IsNaN(value.Confidence) || math.IsInf(value.Confidence, 0) {
			continue
		}
		if position, exists := positions[key]; exists {
			if value.Confidence > result[position].Confidence {
				result[position] = value
			}
			continue
		}
		positions[key] = len(result)
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func ContextValues(values []InferredContext) []domain.Context {
	result := make([]domain.Context, 0, len(values))
	for _, value := range values {
		result = append(result, domain.Context{Key: value.Key, Value: value.Value})
	}
	return result
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
