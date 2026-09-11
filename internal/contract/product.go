package contract

import (
	"context"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

// OrganizationState is the stable product-level view of derived organization.
// It deliberately exposes neither the derived store nor a kernel implementation.
type OrganizationState struct {
	Relations      string `json:"relations,omitempty"`
	Status         string `json:"status"`
	Provider       string `json:"provider,omitempty"`
	Error          string `json:"error,omitempty"`
	RequiredAction string `json:"required_action,omitempty"`
}

type MutationResult struct {
	Information  domain.Information `json:"information"`
	Organization OrganizationState  `json:"organization"`
}

type MutationBatchResult struct {
	Result *MutationResult `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type CreateInput struct {
	Kind      domain.InformationKind
	Content   string
	Contexts  []domain.Context
	Relations []domain.ExplicitRelation
	Source    domain.Source
}

type UpdateInput struct {
	ID               string
	ExpectedRevision uint64
	Kind             *domain.InformationKind
	Content          *string
	Contexts         *[]domain.Context
	Relations        *[]domain.ExplicitRelation
	Source           *domain.Source
}

type SearchInput struct {
	Query                    string
	Contexts                 []domain.Context
	Limit                    int
	DisableRelationExpansion bool
}

type SearchResult struct {
	Relations []RelationEvidence         `json:"relations,omitempty"`
	ID        string                     `json:"id"`
	Kind      domain.InformationKind     `json:"kind"`
	Summary   string                     `json:"summary"`
	Evidence  []domain.EvidenceReference `json:"evidence,omitempty"`
	Contexts  []domain.Context           `json:"contexts,omitempty"`
	Score     float64                    `json:"score"`
	Signals   []string                   `json:"signals"`
}

type EvidenceSearchInput struct {
	SourceID string
	Query    string
	Limit    int
}

type NavigationNode struct {
	ID        string                 `json:"id"`
	Kind      domain.InformationKind `json:"kind"`
	Summary   string                 `json:"summary"`
	Contexts  []domain.Context       `json:"contexts,omitempty"`
	Cues      []semantics.Cue        `json:"cues,omitempty"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// NavigationEdge is a product result, not a derived-index record.
type NavigationEdge struct {
	Grounded   *RelationEvidence `json:"grounded,omitempty"`
	SourceID   string            `json:"source_id"`
	TargetID   string            `json:"target_id"`
	Type       string            `json:"type"`
	Confidence float64           `json:"confidence,omitempty"`
	Evidence   string            `json:"evidence,omitempty"`
	Depth      int               `json:"depth"`
}

type NavigationResult struct {
	Continuation string           `json:"continuation,omitempty"`
	Incomplete   bool             `json:"incomplete,omitempty"`
	Nodes        []NavigationNode `json:"nodes"`
	Edges        []NavigationEdge `json:"edges"`
}

// RelationEvidence keeps the same source-read contract as ordinary evidence.
// A path locates material; it does not assert a transitive conclusion.
type RelationEvidence struct {
	ID         string                     `json:"id"`
	Origin     string                     `json:"origin"`
	Type       string                     `json:"type"`
	Meaning    string                     `json:"meaning"`
	Source     domain.EvidenceReference   `json:"source"`
	Target     domain.EvidenceReference   `json:"target"`
	Context    []domain.EvidenceReference `json:"context,omitempty"`
	Conditions []domain.EvidenceReference `json:"conditions,omitempty"`
}

type SemanticSubmissionResult struct {
	WorkID       string            `json:"work_id"`
	Organization OrganizationState `json:"organization,omitempty"`
	Error        string            `json:"error,omitempty"`
}

// ProductCapability is the versioned, in-process product waist used by access
// adapters. Its values describe product semantics and contain no storage,
// model, protocol, or deployment types.
type ProductCapability interface {
	Rules(context.Context) string
	Create(context.Context, CreateInput) (MutationResult, error)
	CreateBatch(context.Context, []CreateInput) ([]MutationBatchResult, error)
	Update(context.Context, UpdateInput) (MutationResult, error)
	Read(context.Context, string) (domain.Information, error)
	ReadEvidence(context.Context, string) (domain.Evidence, error)
	ReadInformation(context.Context, string) (InformationRead, error)
	ReadEvidenceWithBasis(context.Context, string) (EvidenceRead, error)
	CheckInformation(context.Context, []string) ([]InformationCheck, error)
	SearchEvidence(context.Context, EvidenceSearchInput) ([]domain.EvidenceReference, error)
	Search(context.Context, SearchInput) ([]SearchResult, error)
	Navigate(context.Context, []string, []string, int, int) (NavigationResult, error)
	SemanticWork(context.Context, int) ([]semantics.Work, error)
	SemanticWorkFor(context.Context, []string) ([]semantics.Work, error)
	SubmitSemantic(context.Context, semantics.Submission) (OrganizationState, error)
	SubmitSemanticBatch(context.Context, []semantics.Submission) ([]SemanticSubmissionResult, error)
	SemanticStatus() map[string]int
	Organization(string) (OrganizationState, error)
}
