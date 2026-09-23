package contract

import (
	"context"
	"github.com/HJSunDev/ownward/internal/domain"
)

const BasisSchema = "ownward.information-basis/v1"
const MaxCheckItems = 64

// SourceSnapshot 的正文、指纹和直接说明来自同一次权威读取。
type SourceSnapshot struct {
	Information    domain.Information
	Fingerprint    string
	Clarifications []ClarificationSource
}
type ClarificationSource struct {
	Information domain.Information
	Fingerprint string
	Selector    *domain.TextSelector
}
type SourceAuthority interface {
	ReadSource(string) (SourceSnapshot, bool, error)
}

type Clarification struct {
	SourceID       string                    `json:"source_id"`
	SourceRevision uint64                    `json:"source_revision"`
	Evidence       *domain.EvidenceReference `json:"evidence,omitempty"`
	Covered        bool                      `json:"covered"`
}
type InformationRead struct {
	Information    domain.Information `json:"information"`
	Basis          string             `json:"basis"`
	Clarifications []Clarification    `json:"clarifications"`
	Original       *OriginalEvidence  `json:"original,omitempty"`
}

// ReadOptions requests retained source evidence in addition to current use.
// Basis continues to describe Information, not the historical Original.
type ReadOptions struct {
	IncludeOriginal bool `json:"include_original,omitempty"`
}

// OriginalEvidence identifies the first retained source, not edit history.
// Content and Source are present only when explicitly requested.
type OriginalEvidence struct {
	Revision uint64         `json:"revision"`
	Content  *string        `json:"content,omitempty"`
	Source   *domain.Source `json:"source,omitempty"`
}
type EvidenceRead struct {
	Evidence       domain.Evidence   `json:"evidence"`
	Basis          string            `json:"basis"`
	Clarifications []Clarification   `json:"clarifications"`
	Original       *OriginalEvidence `json:"original,omitempty"`
}
type InformationCheck struct {
	Basis          string          `json:"basis"`
	Status         string          `json:"status"`
	SourceID       string          `json:"source_id,omitempty"`
	Clarifications []Clarification `json:"clarifications,omitempty"`
}
type systemKey struct{}

func WithInformationSystem(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, systemKey{}, id)
}
func InformationSystem(ctx context.Context) string {
	id, _ := ctx.Value(systemKey{}).(string)
	return id
}
