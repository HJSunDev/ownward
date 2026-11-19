package core

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/productrules"
	"github.com/HJSunDev/ownward/internal/semantics"
)

const CollaborationRules = productrules.Collaboration

var ErrSemanticConflict = errors.New("当前语义工作已经接受了不同结果")

const semanticWorkRequiredAction = "ownward_semantic_work"

// A byte bound is deliberately used instead of a tokenizer-specific estimate:
// byte length is a deterministic upper bound for byte-fallback tokenization and
// keeps every request comfortably below the 512-token production embedding
// window. Long assets remain authoritative and are represented after semantic
// submission rather than being truncated for the embedding transport.
const semanticEmbeddingChunkBytes = 320

type OrganizationState = contract.OrganizationState
type MutationResult = contract.MutationResult
type MutationBatchResult = contract.MutationBatchResult
type CreateInput = contract.CreateInput
type UpdateInput = contract.UpdateInput
type SearchInput = contract.SearchInput
type SearchResult = contract.SearchResult
type EvidenceSearchInput = contract.EvidenceSearchInput
type NavigationNode = contract.NavigationNode
type NavigationResult = contract.NavigationResult
type SemanticSubmissionResult = contract.SemanticSubmissionResult

type informationBasis struct {
	Schema   string `json:"v"`
	System   string `json:"s"`
	ID       string `json:"i"`
	Revision uint64 `json:"r"`
	Content  string `json:"c"`
	Notes    string `json:"n"`
	Start    int    `json:"b"`
	End      int    `json:"e"`
}

func fusedSignalPriority(signals map[string]struct{}) int {
	priority := 0
	if _, exists := signals["semantic"]; exists {
		priority++
	}
	if _, exists := signals["lexical"]; exists {
		priority += 2
	}
	return priority
}

func resultSignalPriority(signals []string) int {
	priority := 0
	if contains(signals, "semantic") {
		priority++
	}
	if contains(signals, "lexical") {
		priority += 2
	}
	return priority
}

func normalizeContexts(values []domain.Context) []domain.Context {
	result := make([]domain.Context, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value.Key = strings.TrimSpace(value.Key)
		value.Value = strings.TrimSpace(value.Value)
		key := strings.ToLower(value.Key) + "\x00" + strings.ToLower(value.Value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func mergeContexts(left, right []domain.Context) []domain.Context {
	explicit := normalizeContexts(left)
	explicitKeys := make(map[string]struct{}, len(explicit))
	for _, value := range explicit {
		explicitKeys[strings.ToLower(value.Key)] = struct{}{}
	}
	merged := append([]domain.Context(nil), explicit...)
	for _, value := range normalizeContexts(right) {
		if _, exists := explicitKeys[strings.ToLower(value.Key)]; exists {
			continue
		}
		merged = append(merged, value)
	}
	return normalizeContexts(merged)
}

func truncate(value string, length int) string {
	runes := []rune(value)
	if len(runes) <= length {
		return value
	}
	return strings.TrimSpace(string(runes[:length])) + "…"
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func newID(now time.Time) (string, error) {
	var random [10]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("生成信息标识: %w", err)
	}
	milliseconds := uint64(now.UnixMilli())
	var raw [16]byte
	raw[0] = byte(milliseconds >> 40)
	raw[1] = byte(milliseconds >> 32)
	raw[2] = byte(milliseconds >> 24)
	raw[3] = byte(milliseconds >> 16)
	raw[4] = byte(milliseconds >> 8)
	raw[5] = byte(milliseconds)
	copy(raw[6:], random[:])
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80
	buffer := make([]byte, 36)
	hex.Encode(buffer[0:8], raw[0:4])
	buffer[8] = '-'
	hex.Encode(buffer[9:13], raw[4:6])
	buffer[13] = '-'
	hex.Encode(buffer[14:18], raw[6:8])
	buffer[18] = '-'
	hex.Encode(buffer[19:23], raw[8:10])
	buffer[23] = '-'
	hex.Encode(buffer[24:36], raw[10:16])
	return string(buffer), nil
}

func boundedEmbeddingBatchEnd(values []string, start int) int {
	end, total := start, 0
	for end < len(values) && end-start < 32 {
		size := len([]byte(values[end]))
		if end > start && total+size > semanticEmbeddingChunkBytes {
			break
		}
		total += size
		end++
	}
	return end
}

func semanticEmbeddingChunks(analysis semantics.Analysis) []string {
	lines := []string{"summary: " + analysis.Summary}
	for _, topic := range analysis.Topics {
		lines = append(lines, "topic: "+topic)
	}
	for _, cue := range analysis.Cues {
		lines = append(lines, "cue "+cue.Kind+": "+cue.Text)
	}
	for _, inferred := range analysis.Contexts {
		lines = append(lines, "context "+inferred.Key+"="+inferred.Value+": "+inferred.Evidence)
	}
	for _, relation := range analysis.Relations {
		lines = append(lines, "relation "+relation.Direction+" "+relation.Type+" "+relation.TargetID+": "+relation.Evidence)
	}
	chunks := make([]string, 0, len(lines))
	current := ""
	flush := func() {
		if strings.TrimSpace(current) != "" {
			chunks = append(chunks, current)
			current = ""
		}
	}
	for _, line := range lines {
		for _, part := range splitUTF8ByBytes(strings.TrimSpace(line), semanticEmbeddingChunkBytes) {
			if current == "" {
				current = part
				continue
			}
			if len([]byte(current))+1+len([]byte(part)) <= semanticEmbeddingChunkBytes {
				current += "\n" + part
				continue
			}
			flush()
			current = part
		}
	}
	flush()
	return chunks
}

func splitUTF8ByBytes(value string, maximum int) []string {
	if value == "" {
		return nil
	}
	parts := make([]string, 0, len([]byte(value))/maximum+1)
	start, size := 0, 0
	for index, current := range value {
		width := len(string(current))
		if size > 0 && size+width > maximum {
			parts = append(parts, value[start:index])
			start, size = index, 0
		}
		size += width
	}
	parts = append(parts, value[start:])
	return parts
}

func aggregateSemanticVectors(vectors [][]float32) ([]float32, error) {
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return nil, errors.New("语义检索表示没有向量")
	}
	result := make([]float32, len(vectors[0]))
	for _, vector := range vectors {
		if len(vector) != len(result) {
			return nil, errors.New("语义检索表示向量维度不一致")
		}
		for index, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, errors.New("语义检索表示包含非有限向量")
			}
			result[index] += value
		}
	}
	norm := float64(0)
	for _, value := range result {
		norm += float64(value * value)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return nil, fmt.Errorf("语义检索表示聚合为零向量")
	}
	for index := range result {
		result[index] = float32(float64(result[index]) / norm)
	}
	return result, nil
}

func organizationState(record derived.Record) OrganizationState {
	state := OrganizationState{Status: record.Status, Provider: record.Provider, Error: record.Error}
	if record.Analysis.Organization != nil {
		state.Relations = "organized_given_material"
	} else {
		state.Relations = "not_organized"
	}
	if record.Status == "pending" && record.HasPendingSemanticWork() {
		state.RequiredAction = semanticWorkRequiredAction
	}
	return state
}

// validateSemanticSubmissionBinding checks only trust-boundary identity and
// evidence binding. It deliberately does not normalize a valid analysis, so
// an external capability cannot decide organization state.
func validateSemanticSubmissionBinding(work semantics.Work, declared semantics.Capability, submission semantics.Submission) error {
	if submission.Schema != semantics.SubmissionSchema || submission.WorkID != work.ID ||
		submission.AssetID != work.Asset.ID || submission.Revision != work.Asset.Revision {
		return errors.New("语义结果与当前工作或资产版本不一致")
	}
	if submission.Status != semantics.SubmissionComplete {
		return errors.New("语义结果未声明完整完成")
	}
	if submission.Capability != declared {
		return errors.New("语义结果能力身份与装配声明不一致")
	}
	candidates := make(map[string]uint64, len(work.Candidates))
	for _, candidate := range work.Candidates {
		candidates[candidate.ID] = candidate.Revision
	}
	explicit := make(map[string]struct{}, len(work.Asset.Relations))
	for _, relation := range work.Asset.Relations {
		explicit[relation.Type+"\x00"+relation.TargetID] = struct{}{}
	}
	for _, relation := range submission.Analysis.Relations {
		revision, exists := candidates[relation.TargetID]
		if !exists || relation.TargetID == work.Asset.ID {
			return errors.New("语义关系未绑定当前工作候选")
		}
		if relation.TargetRevision != 0 && relation.TargetRevision != revision {
			return errors.New("语义关系候选版本与当前工作不一致")
		}
		if !semantics.IsAllowedRelationType(relation.Type) ||
			(relation.Direction != "" && !semantics.IsAllowedRelationDirection(relation.Direction)) ||
			relation.Confidence < 0.75 || relation.Confidence > 1 || math.IsNaN(relation.Confidence) || math.IsInf(relation.Confidence, 0) {
			return errors.New("语义关系类型、方向或置信度无效")
		}
		_, authoritative := explicit[relation.Type+"\x00"+relation.TargetID]
		if strings.TrimSpace(relation.Evidence) == "" && (!authoritative || relation.Direction == "incoming") {
			return errors.New("语义关系缺少当前工作证据")
		}
	}
	return nil
}
