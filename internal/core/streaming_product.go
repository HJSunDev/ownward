package core

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

var _ contract.ProductCapability = (*StreamingAssets)(nil)
var _ contract.KernelLifecycle = (*StreamingAssets)(nil)

// The typed port is retained for small in-process callers. Protocol adapters
// use ExecuteStream directly so neither requests nor answers are materialized.
func streamValue[T any](ctx context.Context, s *StreamingAssets, operation string, input any) (T, error) {
	var out T
	doc, e := streamjson.Build(ctx, s.Scratch, s.Budget, s.DiskBytes, func(w io.Writer) error { return json.NewEncoder(w).Encode(input) })
	if e != nil {
		return out, e
	}
	defer doc.Close()
	if operation == "ownward_create" || operation == "ownward_create_batch" || operation == "ownward_update" {
		op, ok := contract.Operation(ctx)
		if !ok {
			var id [24]byte
			if _, e = rand.Read(id[:]); e != nil {
				return out, e
			}
			op.ID = hex.EncodeToString(id[:])
			op.Generation, e = s.Store.OperationGeneration(ctx)
			if e != nil {
				return out, e
			}
		}
		h := sha256.New()
		io.WriteString(h, operation+":")
		if e = doc.Root().Canonical(ctx, h); e != nil {
			return out, e
		}
		op.Kind = operation
		op.Digest = hex.EncodeToString(h.Sum(nil))
		ctx = contract.WithOperation(ctx, op)
	}
	result, e := s.ExecuteStream(ctx, contract.StreamRequest{Operation: operation, Arguments: streamjson.RawSource{Node: doc.Root()}})
	if e != nil {
		return out, e
	}
	defer result.Close()
	r, e := result.Value.Open(ctx)
	if e != nil {
		return out, e
	}
	defer r.Close()
	e = json.NewDecoder(r).Decode(&out)
	if e != nil {
		return out, e
	}
	if result.Check != nil {
		e = result.Check(ctx)
	}
	return out, e
}
func (s *StreamingAssets) Rules(context.Context) string { return CollaborationRules }
func createValue(v contract.CreateInput) map[string]any {
	out := map[string]any{"kind": v.Kind, "content": v.Content, "contexts": v.Contexts, "explicit_relations": v.Relations, "source": v.Source}
	if v.OrganizationMode != "" {
		out["organization_mode"] = v.OrganizationMode
	}
	return out
}
func (s *StreamingAssets) Create(ctx context.Context, v contract.CreateInput) (contract.MutationResult, error) {
	out, e := streamValue[struct {
		Result contract.MutationResult `json:"result"`
	}](ctx, s, "ownward_create", createValue(v))
	return out.Result, e
}
func (s *StreamingAssets) CreateBatch(ctx context.Context, v []contract.CreateInput) ([]contract.MutationBatchResult, error) {
	items := make([]map[string]any, len(v))
	for i, x := range v {
		items[i] = createValue(x)
	}
	out, e := streamValue[struct {
		Results []contract.MutationBatchResult `json:"results"`
	}](ctx, s, "ownward_create_batch", map[string]any{"items": items})
	return out.Results, e
}
func (s *StreamingAssets) Update(ctx context.Context, v contract.UpdateInput) (contract.MutationResult, error) {
	input := map[string]any{"id": v.ID, "expected_revision": v.ExpectedRevision}
	if v.OrganizationMode != "" {
		input["organization_mode"] = v.OrganizationMode
	}
	if v.Kind != nil {
		input["kind"] = *v.Kind
	}
	if v.Content != nil {
		input["content"] = *v.Content
	}
	if v.Contexts != nil {
		input["contexts"] = *v.Contexts
	}
	if v.Relations != nil {
		input["explicit_relations"] = *v.Relations
	}
	if v.Source != nil {
		input["source"] = *v.Source
	}
	out, e := streamValue[struct {
		Result contract.MutationResult `json:"result"`
	}](ctx, s, "ownward_update", input)
	return out.Result, e
}
func (s *StreamingAssets) ReadInformation(ctx context.Context, id string) (contract.InformationRead, error) {
	return streamValue[contract.InformationRead](ctx, s, "ownward_read", map[string]any{"id": id})
}
func (s *StreamingAssets) Read(ctx context.Context, id string) (domain.Information, error) {
	out, e := s.ReadInformation(ctx, id)
	return out.Information, e
}
func (s *StreamingAssets) ReadEvidenceWithBasis(ctx context.Context, id string) (contract.EvidenceRead, error) {
	return streamValue[contract.EvidenceRead](ctx, s, "ownward_evidence_read", map[string]any{"id": id})
}
func (s *StreamingAssets) ReadEvidence(ctx context.Context, id string) (domain.Evidence, error) {
	out, e := s.ReadEvidenceWithBasis(ctx, id)
	return out.Evidence, e
}
func (s *StreamingAssets) Search(ctx context.Context, v contract.SearchInput) ([]contract.SearchResult, error) {
	out, e := streamValue[struct {
		Results []contract.SearchResult `json:"results"`
	}](ctx, s, "ownward_search", map[string]any{"query": v.Query, "contexts": v.Contexts, "limit": v.Limit})
	return out.Results, e
}
func (s *StreamingAssets) SearchEvidence(ctx context.Context, v contract.EvidenceSearchInput) ([]domain.EvidenceReference, error) {
	out, e := streamValue[struct {
		Evidence []domain.EvidenceReference `json:"evidence"`
	}](ctx, s, "ownward_evidence_search", map[string]any{"source_id": v.SourceID, "query": v.Query, "limit": v.Limit})
	return out.Evidence, e
}
func (s *StreamingAssets) Navigate(ctx context.Context, ids, types []string, depth, limit int) (contract.NavigationResult, error) {
	out, e := streamValue[struct {
		Result contract.NavigationResult `json:"result"`
	}](ctx, s, "ownward_navigate", map[string]any{"start_ids": ids, "relation_types": types, "depth": depth, "limit": limit})
	return out.Result, e
}
func (s *StreamingAssets) SemanticWork(ctx context.Context, limit int) ([]semantics.Work, error) {
	return s.typedSemanticWork(ctx, map[string]any{"limit": limit})
}
func (s *StreamingAssets) SemanticWorkFor(ctx context.Context, ids []string) ([]semantics.Work, error) {
	return s.typedSemanticWork(ctx, map[string]any{"asset_ids": ids})
}
func (s *StreamingAssets) typedSemanticWork(ctx context.Context, args any) ([]semantics.Work, error) {
	out, e := streamValue[struct {
		Work []semantics.Work `json:"work"`
	}](ctx, s, "ownward_semantic_work", args)
	return out.Work, e
}
func (s *StreamingAssets) SubmitSemantic(ctx context.Context, value semantics.Submission) (contract.OrganizationState, error) {
	out, e := streamValue[struct {
		Organization contract.OrganizationState `json:"organization"`
	}](ctx, s, "ownward_semantic_submit", map[string]any{"submission": value})
	return out.Organization, e
}
func (s *StreamingAssets) SubmitSemanticBatch(ctx context.Context, values []semantics.Submission) ([]contract.SemanticSubmissionResult, error) {
	out, e := streamValue[struct {
		Results []contract.SemanticSubmissionResult `json:"results"`
	}](ctx, s, "ownward_semantic_submit_batch", map[string]any{"submissions": values})
	return out.Results, e
}
func (s *StreamingAssets) CheckInformation(ctx context.Context, bases []string) ([]contract.InformationCheck, error) {
	out, e := streamValue[struct {
		Results []contract.InformationCheck `json:"results"`
	}](ctx, s, "ownward_check", map[string]any{"bases": bases})
	return out.Results, e
}
func (s *StreamingAssets) Organization(id string) (contract.OrganizationState, error) {
	ctx := context.Background()
	if _, e := s.Store.ReadAssetMeta(ctx, id, 0); e != nil {
		return contract.OrganizationState{}, e
	}
	if s.Embedder == nil {
		return contract.OrganizationState{Status: "not_enabled"}, nil
	}
	g, _, e := s.Store.Generation(ctx)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return contract.OrganizationState{}, e
	}
	if e == nil {
		v, e := s.Store.CurrentOrganization(ctx, g, id)
		if e == nil {
			r, e := s.Store.RecordHeader(ctx, v)
			if e != nil {
				return contract.OrganizationState{}, e
			}
			state := organizationState(r)
			if state.Status == "pending" {
				if managed, e := s.Store.DeferredOrganization(ctx, id); e != nil {
					return contract.OrganizationState{}, e
				} else if managed {
					state.RequiredAction = "ownward_semantic_jobs"
				}
			}
			return state, nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return contract.OrganizationState{}, e
		}
	}
	action := semanticWorkRequiredAction
	if managed, e := s.Store.DeferredOrganization(ctx, id); e != nil {
		return contract.OrganizationState{}, e
	} else if managed {
		action = "ownward_semantic_jobs"
	}
	return contract.OrganizationState{Status: "pending", Provider: "external-semantic-capability", RequiredAction: action}, nil
}
func (s *StreamingAssets) SemanticStatus() map[string]int {
	out, _ := s.Store.SemanticCounts(context.Background())
	return out
}
func (s *StreamingAssets) Maintain(ctx context.Context, rebuild bool) (map[string]int, error) {
	if rebuild {
		if e := s.rebuildStreaming(ctx); e != nil {
			return nil, e
		}
	}
	if e := s.Store.DrainMaintenance(ctx); e != nil {
		return nil, e
	}
	return s.Store.SemanticCounts(ctx)
}
func (s *StreamingAssets) Close() error {
	return errors.Join(s.cleanDeliveryMaterials(true), s.Store.Close())
}

func (s *StreamingAssets) smallResult(ctx context.Context, value any) (*contract.StreamResult, error) {
	stamp, e := s.Store.RetrievalStamp(ctx)
	if e != nil {
		return nil, e
	}
	doc, e := streamjson.Build(ctx, s.Scratch, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes, func(w io.Writer) error { return json.NewEncoder(w).Encode(value) })
	if e != nil {
		return nil, e
	}
	return s.readResult(ctx, doc, stamp), nil
}
