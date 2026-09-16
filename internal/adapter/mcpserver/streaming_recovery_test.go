package mcpserver

import (
	"context"
	"errors"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
	"strings"
	"testing"
)

type streamRecoveryVector struct {
	streamTestVector
	fail bool
}

func (v *streamRecoveryVector) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if v.fail {
		return nil, errors.New("review: temporary local embedding failure")
	}
	return v.streamTestVector.EmbedDocuments(ctx, texts)
}
func streamRecoverySubmission(w semantics.Work) semantics.Submission {
	return semantics.Submission{Schema: semantics.SubmissionSchema, WorkID: w.ID, AssetID: w.Asset.ID, Revision: w.Asset.Revision, Capability: semantics.Capability{ID: "fixed-fixture", Version: "1"}, Status: semantics.SubmissionComplete, Analysis: semantics.Analysis{Summary: "Fixed source summary.", Organization: &semantics.Organization{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{{ID: "whole"}}, Links: []semantics.GroundedLink{}}}}
}
func TestStreamingAcceptedResultNotRedispatched(t *testing.T) {
	vectors := &streamRecoveryVector{fail: true}
	f := newStreamReplayFixture(t, vectors)
	var created CreateOutput
	f.decode(f.call("ownward_create", "save", map[string]any{"content": strings.Repeat("long source. ", 40)}), &created)
	id := created.Result.Information.ID
	var work SemanticWorkOutput
	f.decode(f.call("ownward_semantic_work", "", map[string]any{"asset_ids": []string{id}}), &work)
	if len(work.Work) != 1 {
		t.Fatalf("initial work count %d", len(work.Work))
	}
	sub := streamRecoverySubmission(work.Work[0])
	var accepted SemanticSubmitOutput
	f.decode(f.call("ownward_semantic_submit", "submit", map[string]any{"submission": sub}), &accepted)
	if accepted.Organization.Status != "pending" {
		t.Fatal("expected local-vector pending", accepted)
	}
	var queued SemanticWorkOutput
	f.decode(f.call("ownward_semantic_work", "", map[string]any{"limit": 20}), &queued)
	if len(queued.Work) != 0 {
		t.Fatalf("accepted result remains in AI queue: %d", len(queued.Work))
	}
	var after SemanticWorkOutput
	f.decode(f.call("ownward_semantic_work", "", map[string]any{"asset_ids": []string{id}}), &after)
	t.Logf("accepted status=%s required_action=%s; subsequent work count=%d", accepted.Organization.Status, accepted.Organization.RequiredAction, len(after.Work))
	vectors.fail = false
	var recovered SemanticSubmitOutput
	f.decode(f.call("ownward_semantic_submit", "retry", map[string]any{"submission": sub}), &recovered)
	if recovered.Organization.Status != "ready" {
		t.Fatalf("saved-result recovery failed: %+v", recovered)
	}
	if len(after.Work) != 0 {
		t.Errorf("already accepted semantic result dispatched again; only local vector remains")
	}
}
func TestStreamingExplicitRelationWinsIncomingInference(t *testing.T) {
	f := newStreamReplayFixture(t, streamTestVector{})
	var b CreateOutput
	f.decode(f.call("ownward_create", "b", map[string]any{"content": "Budget document."}), &b)
	var a CreateOutput
	f.decode(f.call("ownward_create", "a", map[string]any{"content": "Budget explanation.", "explicit_relations": []domain.ExplicitRelation{{Type: "supports", TargetID: b.Result.Information.ID}}}), &a)
	id := a.Result.Information.ID
	var works SemanticWorkOutput
	f.decode(f.call("ownward_semantic_work", "", map[string]any{"asset_ids": []string{id}}), &works)
	if len(works.Work) != 1 {
		t.Fatalf("work count %d", len(works.Work))
	}
	sub := streamRecoverySubmission(works.Work[0])
	sub.Analysis.Relations = []semantics.Relation{{Type: "contradicts", TargetID: b.Result.Information.ID, TargetRevision: 1, Confidence: 1, Direction: "incoming", Evidence: "Budget evidence."}}
	var accepted SemanticSubmitOutput
	f.decode(f.call("ownward_semantic_submit", "submit", map[string]any{"submission": sub}), &accepted)
	var nav NavigateOutput
	f.decode(f.call("ownward_navigate", "", map[string]any{"start_ids": []string{id}}), &nav)
	t.Logf("navigation edges=%+v", nav.Result.Edges)
	for _, edge := range nav.Result.Edges {
		if edge.Type == "contradicts" {
			t.Errorf("incoming inference overrides explicit target priority: %+v", edge)
		}
	}
}
