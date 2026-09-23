package contract

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
)

const OwnerViewSchema = "ownward.owner-view/v1"

// These are protocol bounds, shared by every owner client. Cursor invalidates
// projections on authority or visibility changes. After/Next are bounded page
// positions; for events they resume the append stream, including an empty tail.
// A forget barrier invalidates event positions so clients clear obsolete views.
// Poll active windows at 1–30 seconds; save edits no more than once per second.
const (
	OwnerPageLimit        = 100
	OwnerTextBytes        = 64 << 10
	OwnerRequestBytes     = 1 << 20
	OwnerDraftUploadBytes = 256 << 20 // streamed UTF-8 replacement; old draft remains until complete
	OwnerPollMin          = time.Second
	OwnerPollMax          = 30 * time.Second
	OwnerAutosaveMin      = time.Second
	OwnerHistoryDays      = 30
	OwnerHistoryItems     = 4096
)

var ErrOwnerRefresh = errors.New("内容或权限已变化，请保留未保存输入并刷新")

// Query and Action use opaque, typed handles. Clients must not parse them.
// Text uses UTF-8 byte offsets; each page ends at a character boundary.
// All list results are bounded, including empty pages with a continuation.
// Views: changes, assets, continuable, drafts, draft_grants, connections, pending, history,
// events, content, details, original, original_details, draft_content, relations,
// relation_text, overview, health, publish_receipt, resolve, source, recent.
// resolve accepts a read-only Reference (or a current typed handle); references
// survive transport restarts, but never grant authority or act as write handles.
// recent pages newest first; events retains its existing incremental order.
// Details and relation_text page raw JSON;
// concatenate all text pages before decoding. Find matches any indexed query
// token and can filter by kind/state; it is deterministic, not semantic search.
type OwnerQuery struct {
	Reference   string                 `json:"reference,omitempty"`    // resolve only: read locator, never write authority
	OperationID string                 `json:"operation_id,omitempty"` // publish_receipt only; strictly read-only
	View        string                 `json:"view"`
	Handle      string                 `json:"handle,omitempty"`
	Cursor      string                 `json:"cursor,omitempty"`
	After       string                 `json:"after,omitempty"`
	Query       string                 `json:"query,omitempty"`
	State       string                 `json:"state,omitempty"`
	Kind        domain.InformationKind `json:"kind,omitempty"`
	Limit       int                    `json:"limit,omitempty"`
	Offset      int64                  `json:"offset,omitempty"`
}

type OwnerAsset struct {
	Version     string                 `json:"version"` // equality token only, never a write precondition
	Reference   string                 `json:"reference"`
	Handle      string                 `json:"handle"`
	Kind        domain.InformationKind `json:"kind"`
	State       string                 `json:"state"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
	Bytes       int64                  `json:"bytes"`
	HasOriginal bool                   `json:"has_original"`
}

type OwnerDraft struct {
	Version         string    `json:"version"`
	Reference       string    `json:"reference"`
	TargetReference string    `json:"target_reference,omitempty"`
	Handle          string    `json:"handle"`
	Target          string    `json:"target,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
	Bytes           int64     `json:"bytes"`
}

type OwnerText struct {
	Text       string `json:"text"`
	NextOffset int64  `json:"next_offset"`
	More       bool   `json:"more"`
}

type OwnerConnection struct {
	Handle      string       `json:"handle"`
	Name        string       `json:"name"`
	Distinction string       `json:"distinction"` // stable registration order, not a credential/ID
	Permissions []Permission `json:"permissions"`
	Owner       bool         `json:"owner"`
}

type OwnerGrant struct {
	Handle     string          `json:"handle"`
	Draft      string          `json:"draft"`
	Connection OwnerConnection `json:"connection"`
	ExpiresAt  time.Time       `json:"expires_at"`
}

type OwnerDecision struct {
	Handle       string       `json:"handle"`
	Kind         string       `json:"kind"`
	State        string       `json:"state"`
	Subject      string       `json:"subject,omitempty"`
	Distinction  string       `json:"distinction,omitempty"`
	Verification string       `json:"verification,omitempty"` // public target-proof check, never a credential
	Permissions  []Permission `json:"permissions,omitempty"`
	Targets      []string     `json:"targets,omitempty"`
	Consequence  string       `json:"consequence"`
	At           *time.Time   `json:"at,omitempty"` // historical decisions are read-only
}

type OwnerActivity struct {
	Kind                 string                      `json:"kind"`
	State                string                      `json:"state,omitempty"`
	Asset                string                      `json:"asset,omitempty"`
	Unavailable          bool                        `json:"unavailable,omitempty"`
	At                   time.Time                   `json:"at"`
	Subject              string                      `json:"subject,omitempty"`
	Distinction          string                      `json:"distinction,omitempty"`
	Permissions          []Permission                `json:"permissions,omitempty"`
	TargetCount          int                         `json:"target_count,omitempty"`
	UnavailableTargets   int                         `json:"unavailable_targets,omitempty"`
	InvalidatedRelations *RelationInvalidationCounts `json:"invalidated_relations,omitempty"`
}

type OwnerBasis struct {
	Asset     string `json:"asset"`
	StartRune int64  `json:"start_rune"`
	EndRune   int64  `json:"end_rune"`
	Role      string `json:"role"`
}

type OwnerRelation struct {
	Type     string       `json:"type"`
	Source   string       `json:"source"`
	Target   string       `json:"target"`
	Meaning  string       `json:"meaning,omitempty"` // handle for the complete, paged explanation
	Evidence string       `json:"evidence,omitempty"`
	Basis    []OwnerBasis `json:"basis,omitempty"`
}

type OwnerOverview struct {
	Examined    int            `json:"examined"`
	Connected   int            `json:"connected"`
	Unconnected int            `json:"unconnected"`
	Pending     int            `json:"pending"`
	Unresolved  int            `json:"unresolved"`
	Types       map[string]int `json:"types"`
	Approximate bool           `json:"approximate"`
}

type OwnerPage struct {
	Source       *OwnerSource      `json:"source,omitempty"`
	Unavailable  bool              `json:"unavailable,omitempty"` // resolve: forgotten/discarded, not a transient read error
	Schema       string            `json:"schema"`
	Cursor       string            `json:"cursor"`
	Changed      bool              `json:"changed"`
	Next         string            `json:"next,omitempty"`
	Assets       []OwnerAsset      `json:"assets,omitempty"`
	Drafts       []OwnerDraft      `json:"drafts,omitempty"`
	Connections  []OwnerConnection `json:"connections,omitempty"`
	Grants       []OwnerGrant      `json:"grants,omitempty"`
	Decisions    []OwnerDecision   `json:"decisions,omitempty"`
	Activity     []OwnerActivity   `json:"activity,omitempty"`
	Relations    []OwnerRelation   `json:"relations,omitempty"`
	Text         *OwnerText        `json:"text,omitempty"`
	Overview     *OwnerOverview    `json:"overview,omitempty"`
	Health       string            `json:"health,omitempty"`
	Organization string            `json:"organization,omitempty"`
	Reset        bool              `json:"reset,omitempty"`
	Publication  *OwnerPublication `json:"publication,omitempty"`
}

type OwnerSource struct {
	Actor            string `json:"actor,omitempty"`
	Ref              string `json:"ref,omitempty"`
	Authored         bool   `json:"authored"`
	PreserveOriginal bool   `json:"preserve_original"`
}

// Publication recovery never writes or revives a draft. completed means the
// committed revision is current; changed links to its newer current revision;
// unavailable has no openable asset. unknown includes expired receipts and
// must never be interpreted as permission to automatically publish again.
type OwnerPublication struct {
	State string `json:"state"`
	Asset string `json:"asset,omitempty"`
}

// OperationID must be reused for an uncertain publish/management retry. Failed
// conditional writes preserve the draft; the client keeps its unsaved input.
// If a publish reply is lost, query publish_receipt with the same OperationID,
// including after reconnect/restart. Refreshing an expired draft handle cannot
// recover a draft already removed by a successful publication.
type OwnerAction struct {
	Action      string       `json:"action"`
	Handle      string       `json:"handle,omitempty"`
	Target      string       `json:"target,omitempty"`
	OperationID string       `json:"operation_id,omitempty"`
	Text        *string      `json:"text,omitempty"`
	Accept      bool         `json:"accept,omitempty"`
	Permissions []Permission `json:"permissions,omitempty"`
	Seconds     int          `json:"seconds,omitempty"`
}

type OwnerResult struct {
	Reference string `json:"reference,omitempty"`
	Schema    string `json:"schema"`
	Handle    string `json:"handle,omitempty"`
	Grant     string `json:"grant,omitempty"`
	State     string `json:"state"`
	Cursor    string `json:"cursor,omitempty"`
}

type OwnerView interface {
	Query(context.Context, OwnerQuery) (OwnerPage, error)
	Act(context.Context, OwnerAction) (OwnerResult, error)
	// One complete UTF-8 replacement, bounded by OwnerDraftUploadBytes. The
	// browser binding authenticates, checks origin and paces this like JSON
	// writes. Interrupted uploads leave the previous draft intact.
	ReplaceDraft(context.Context, string, io.Reader) (OwnerResult, error)
}

// AgentDraftWork is deliberately separate from ordinary asset capabilities.
// A grant is bound to the authenticated principal, never a bearer substitute.
type AgentDraftRequest struct {
	Action string `json:"action" jsonschema:"read, replace or append; never publishes or lists drafts"`
	Draft  string `json:"draft"`
	Grant  string `json:"grant"`
	Handle string `json:"handle,omitempty" jsonschema:"Current handle returned by read; required for writes"`
	Offset int64  `json:"offset,omitempty"`
	Text   string `json:"text,omitempty"`
}
type AgentDraftResult struct {
	Handle  string     `json:"handle"`
	Content *OwnerText `json:"content,omitempty"`
}
type AgentDraftWork interface {
	DraftWork(context.Context, AgentDraftRequest) (AgentDraftResult, error)
	CheckDraftWork(context.Context, AgentDraftRequest, string) error
}
