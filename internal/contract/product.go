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
	OrganizationMode string
	Kind             domain.InformationKind
	Content          string
	Contexts         []domain.Context
	Relations        []domain.ExplicitRelation
	Source           domain.Source
}

type UpdateInput struct {
	OrganizationMode string
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

// DeferredOrganizationV1 opts a capable host into leased organization work.
// Omission preserves the existing synchronous preparation contract.
const DeferredOrganizationV1 = "deferred-v1"

// OrganizationJobCapability is an opt-in extension of the product contract.
type OrganizationJobCapability interface {
	OrganizationJobs(context.Context, OrganizationJobRequest) (OrganizationJobResult, error)
}

type OrganizationJobRequest struct {
	AfterAssetID string `json:"after_asset_id,omitempty" jsonschema:"宿主遍历待办的资产游标；仅claim有效"`
	Background   bool   `json:"background,omitempty" jsonschema:"后台领取使用同一内核共享的单执行名额"`
	RequestID    string `json:"request_id,omitempty" jsonschema:"claim必填：每次领取使用唯一请求ID；不确定结果的重试沿用原ID"`
	Action       string `json:"action" jsonschema:"claim、renew、release 或 wait"`
	AssetID      string `json:"asset_id,omitempty"`
	Lease        string `json:"lease,omitempty"`
	LeaseSeconds int    `json:"lease_seconds,omitempty" jsonschema:"执行凭据有效期，1 到 600 秒，默认 300 秒"`
	WaitSeconds  int    `json:"wait_seconds,omitempty" jsonschema:"wait 最长等待，0 到 30 秒"`
}

type OrganizationLease struct {
	AssetID    string    `json:"asset_id"`
	Revision   uint64    `json:"revision"`
	Generation string    `json:"generation"`
	Lease      string    `json:"lease"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type OrganizationJobResult struct {
	Available bool               `json:"available"`
	Claim     *OrganizationLease `json:"claim,omitempty"`
}
