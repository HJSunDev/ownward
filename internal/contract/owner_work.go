package contract

import (
	"context"
	"io"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
)

const OwnerWorkSchema = "ownward.owner-work/v1"

// Owner work is durable private text, not a searchable information asset.
// Authentication comes from the trusted connection context, never these inputs.
type Draft struct {
	ID            string                 `json:"id"`
	Revision      uint64                 `json:"revision"`
	Target        AssetVersion           `json:"target,omitempty"`
	Kind          domain.InformationKind `json:"kind"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
	ContentBytes  int64                  `json:"content_bytes"`
	ContentSHA256 string                 `json:"content_sha256"`
}

type DraftInput struct {
	Target  AssetVersion
	Kind    domain.InformationKind
	Content ContentSource
}

type DraftWrite struct {
	ID               string
	ExpectedRevision uint64
	GrantID          string
	Append           bool
	Content          ContentSource
}

type DraftGrant struct {
	ID        string    `json:"id"`
	DraftID   string    `json:"draft_id"`
	Principal string    `json:"principal"`
	ExpiresAt time.Time `json:"expires_at"`
}

type DraftPage struct {
	Items []Draft `json:"items"`
	Next  string  `json:"next,omitempty"`
}

// OwnerWork is a base contract; public browser and agent bindings are separate.
// IDs are opaque. Revisions are mandatory compare-and-swap preconditions.
// A work grant permits only this draft's read/write, not publication or listing.
// Publish retries reuse the same operation ID and draft revision.
// A zero generation lets the base rotate bounded receipts for new publications;
// an explicit expired generation fails instead of silently changing identity.
// Operations lists pending decisions when true, retained completed history otherwise.
// Content streams must be opened outside database read snapshots, using the
// independent caller context. Snapshot contexts are rejected before delivery;
// each stream read takes its own short snapshot and checks current access.
type OwnerWork interface {
	CreateDraft(context.Context, DraftInput) (Draft, error)
	WriteDraft(context.Context, DraftWrite) (Draft, error)
	ReadDraft(context.Context, string, string) (Draft, io.ReadCloser, error)
	ListDrafts(context.Context, string, int) (DraftPage, error)
	GrantDraft(context.Context, string, string, time.Duration) (DraftGrant, error)
	RevokeDraftGrant(context.Context, string) error
	DiscardDraft(context.Context, string, uint64) error
	PublishDraft(context.Context, string, uint64, OperationIdentity) (AssetVersion, error)
	OwnerWorkRevision(context.Context) (uint64, error)
	OwnerEvents(context.Context, uint64, int) (OwnerEventPage, error)
	OwnerOperations(context.Context, string, int, bool) ([]ManagementReceipt, string, error)
	OpenOriginal(context.Context, string, bool) (uint64, io.ReadCloser, error)
}

// OwnerEvent contains references, never copies of original or draft text.
// Event references are optional projection metadata, not the replay identity.
// Oversized non-management references may be omitted; the durable operation
// and its receipt retain their complete identity and remain queryable.
const OwnerEventReferenceBytes = 4 << 10

type OwnerEvent struct {
	Sequence             uint64                      `json:"sequence"`
	Kind                 string                      `json:"kind"`
	Asset                AssetVersion                `json:"asset,omitempty"`
	Operation            string                      `json:"operation,omitempty"`
	Status               string                      `json:"status,omitempty"`
	At                   time.Time                   `json:"at"`
	InvalidatedRelations *RelationInvalidationCounts `json:"invalidated_relations,omitempty"`
	Access               *OwnerAccessFact            `json:"access,omitempty"`
}

// A past decision's public scope, never a credential or an executable approval.
// Kept only with the bounded event that records the authority's commit.
type OwnerAccessFact struct {
	Subject     string       `json:"subject"`
	Permissions []Permission `json:"permissions,omitempty"`
}

// Counts explain invalidated inherited relations without retaining quotations
// or another copy of target identities. Each relation has one reason at most.
type RelationInvalidationCounts struct {
	QuoteMissing      int64 `json:"quote_missing"`
	QuoteAmbiguous    int64 `json:"quote_ambiguous"`
	TargetUnavailable int64 `json:"target_unavailable"`
}

type OwnerEventPage struct {
	Items []OwnerEvent `json:"items"`
	Next  uint64       `json:"next"`
	Reset bool         `json:"reset"`
}
