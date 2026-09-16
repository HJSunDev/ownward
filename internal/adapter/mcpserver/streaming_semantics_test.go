package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
)

type streamTestVector struct{}

func (streamTestVector) Close() error { return nil }

func (streamTestVector) Name() string           { return "test" }
func (streamTestVector) Space() embedding.Space { return embedding.Space{ID: "test-space"} }
func (streamTestVector) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, s := range texts {
		out[i] = make([]float32, 512)
		for _, c := range s {
			out[i][int(c)%512]++
		}
	}
	return out, nil
}
func (v streamTestVector) EmbedQuery(ctx context.Context, s string) ([]float32, error) {
	vs, e := v.EmbedDocuments(ctx, []string{s})
	return vs[0], e
}

func TestStreamingSemanticEndToEnd(t *testing.T) {
	f := newStreamReplayFixture(t, streamTestVector{})
	text := strings.Repeat("背景。", 140) + "\n\n项目负责人是李明。预算为三万元。\n\n" + strings.Repeat("补充资料。", 100)
	var created CreateOutput
	f.decode(f.call("ownward_create", "create", map[string]any{"content": text}), &created)
	id := created.Result.Information.ID
	var work SemanticWorkOutput
	f.decode(f.call("ownward_semantic_work", "work", map[string]any{"asset_ids": []string{id}}), &work)
	if len(work.Work) != 1 || work.Work[0].Asset.Content != text {
		t.Fatalf("missing source work: %+v", work)
	}
	w := work.Work[0]
	sub := semantics.Submission{Schema: semantics.SubmissionSchema, WorkID: w.ID, AssetID: id, Revision: 1, Capability: semantics.Capability{ID: "external", Version: "1"}, Status: semantics.SubmissionComplete, Analysis: semantics.Analysis{Summary: "李明负责项目，预算三万元。", Organization: &semantics.Organization{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{{ID: "owner", Selector: domain.TextSelector{Exact: "项目负责人是李明。"}}, {ID: "budget", Selector: domain.TextSelector{Exact: "预算为三万元。"}}}}}}
	sub.Analysis.Organization.Links = []semantics.GroundedLink{}
	var accepted SemanticSubmitOutput
	f.decode(f.call("ownward_semantic_submit", "submit", map[string]any{"submission": sub}), &accepted)
	if accepted.Organization.Status != "ready" {
		t.Fatalf("not ready: %+v", accepted)
	}
	var replay SemanticSubmitOutput
	f.decode(f.call("ownward_semantic_submit", "submit", map[string]any{"submission": sub}), &replay)
	if replay.Organization != accepted.Organization {
		t.Fatalf("replay changed %+v %+v", accepted, replay)
	}
	var search SearchOutput
	f.decode(f.call("ownward_search", "", map[string]any{"query": "项目负责人", "limit": 1}), &search)
	if len(search.Results) != 1 || search.Results[0].ID != id || len(search.Results[0].Evidence) == 0 {
		t.Fatalf("search: %+v", search)
	}
	var evidence EvidenceReadOutput
	f.decode(f.call("ownward_evidence_read", "", map[string]any{"id": search.Results[0].Evidence[0].ID}), &evidence)
	if !strings.Contains(evidence.Evidence.Content, "李明") {
		t.Fatalf("evidence: %+v", evidence)
	}
	sub.Analysis.Summary = "different"
	if out := f.call("ownward_semantic_submit", "conflict", map[string]any{"submission": sub}); !out.IsError {
		t.Fatal("conflicting submission accepted")
	}
	var updated UpdateOutput
	f.decode(f.call("ownward_update", "update", map[string]any{"id": id, "expected_revision": 1, "content": "项目负责人是张华。"}), &updated)
	if out := f.call("ownward_evidence_read", "", map[string]any{"id": search.Results[0].Evidence[0].ID}); !out.IsError {
		t.Fatal("old evidence still readable")
	}
	if out := f.call("ownward_semantic_submit", "stale", map[string]any{"submission": sub}); !out.IsError {
		t.Fatal("stale work accepted")
	}
}
