package core

import (
	"context"
	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
	"path/filepath"
	"testing"
)

func TestGroundedOrganizationUsesPublicSubmissionSearchReadAndRecovery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := derived.Open(filepath.Join(root, "derived"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := newTestCollaborative(t, assets, store, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	a, err := service.Create(ctx, CreateInput{Content: "Lumen gateway uses method Beacon."})
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.Create(ctx, CreateInput{Content: "Beacon is permitted only indoors."})
	if err != nil {
		t.Fatal(err)
	}
	work, err := service.SemanticWorkFor(ctx, []string{b.Information.ID})
	if err != nil || len(work) != 1 {
		t.Fatal(err)
	}
	org := &semantics.Organization{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{{ID: "u", Statement: "Beacon has a location condition", Selector: domain.TextSelector{Exact: b.Information.Content}, Mentions: []semantics.Mention{{ID: "m", Name: "Beacon", Selector: domain.TextSelector{Exact: "Beacon"}}}}}, Links: []semantics.GroundedLink{{Type: "applies_in", Meaning: "the selected method has this indoor-only condition", Source: semantics.GraphEndpoint{AssetID: a.Information.ID, Selector: domain.TextSelector{Exact: a.Information.Content}}, Target: semantics.GraphEndpoint{AssetID: b.Information.ID, UnitID: "u", Selector: domain.TextSelector{Exact: b.Information.Content}}}}}
	submission := semanticSubmission(work[0], semantics.Analysis{Summary: "Beacon condition", Organization: org})
	if _, err = service.SubmitSemantic(ctx, submission); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SubmitSemantic(ctx, submission); err != nil {
		t.Fatalf("idempotent submission failed: %v", err)
	}
	// Graph-only retrieval must survive a temporarily unavailable vector path.
	for _, id := range []string{a.Information.ID, b.Information.ID} {
		record, _ := store.Get(id)
		record.Embedding = nil
		service.semantic.Upsert(record)
	}
	single, err := service.Search(ctx, SearchInput{Query: "Lumen gateway", Limit: 1})
	if err != nil || len(single) != 1 || single[0].ID != a.Information.ID || len(single[0].Relations) != 0 {
		t.Fatalf("an unselected graph neighbor added irrelevant reading material: %#v %v", single, err)
	}
	hits, err := service.Search(ctx, SearchInput{Query: "Lumen gateway", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range hits {
		for _, relation := range hit.Relations {
			if relation.Origin != "derived_interpretation" {
				t.Fatal("derived relation must not be presented as an authoritative source fact")
			}
			evidence, err := service.ReadEvidence(ctx, relation.Target.ID)
			if err != nil || evidence.Content != b.Information.Content {
				t.Fatalf("relation did not lead to original condition: %#v %v", evidence, err)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("ordinary search did not deliver grounded link: %#v", hits)
	}
	direct, err := service.Search(ctx, SearchInput{Query: "Beacon", Limit: 2})
	if err != nil || len(direct) != 2 {
		t.Fatalf("direct retrieval changed: %#v %v", direct, err)
	}
	for _, hit := range direct {
		if len(hit.Relations) != 0 {
			t.Fatal("direct hits were burdened with redundant relationship explanations")
		}
	}
	withoutGraph, err := service.Search(ctx, SearchInput{Query: "Beacon", Limit: 2, DisableRelationExpansion: true})
	if err != nil || len(withoutGraph) != len(direct) {
		t.Fatal("graph ablation changed direct hit count")
	}
	for i := range direct {
		if direct[i].ID != withoutGraph[i].ID || direct[i].Score != withoutGraph[i].Score || contains(direct[i].Signals, "relation") {
			t.Fatal("an unused relation changed a direct hit or implied independent support")
		}
	}
	if err = service.Close(); err != nil {
		t.Fatal(err)
	}
	assets, err = assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	store, err = derived.Open(filepath.Join(root, "derived"))
	if err != nil {
		t.Fatal(err)
	}
	service, err = newTestCollaborative(t, assets, store, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	nav, err := service.Navigate(ctx, []string{a.Information.ID}, nil, 1, 10)
	if err != nil || len(nav.Edges) != 1 || nav.Edges[0].Grounded == nil {
		t.Fatalf("recovery lost relation: %#v %v", nav, err)
	}
	changed := "Lumen gateway no longer uses that method."
	if _, err = service.Update(ctx, UpdateInput{ID: a.Information.ID, ExpectedRevision: 1, Content: &changed}); err != nil {
		t.Fatal(err)
	}
	nav, err = service.Navigate(ctx, []string{a.Information.ID}, nil, 1, 10)
	if err != nil || len(nav.Edges) != 0 {
		t.Fatalf("source update retained invalid graph: %#v %v", nav, err)
	}
	if _, err = service.SubmitSemantic(ctx, submission); err == nil {
		t.Fatal("late work with outdated input accepted")
	}
}

func TestRelationFromAnotherTopicDoesNotDisplaceDirectQueryEvidence(t *testing.T) {
	ctx := context.Background()
	assets, err := assetlog.Open(filepath.Join(t.TempDir(), "assets"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := derived.Open(filepath.Join(t.TempDir(), "derived"))
	if err != nil {
		t.Fatal(err)
	}
	vector := embedding.HashForTesting{Dimensions: 64}
	service, err := newTestCollaborative(t, assets, store, vector)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	neighbor, _ := service.Create(ctx, CreateInput{Content: "The walnut pie must be refrigerated."})
	direct, _ := service.Create(ctx, CreateInput{Content: "Lumen gateway maintenance instructions."})
	seed, _ := service.Create(ctx, CreateInput{Content: "Lumen gateway uses Beacon.\n\nLunch includes walnut pie."})
	work, err := service.SemanticWorkFor(ctx, []string{seed.Information.ID})
	if err != nil {
		t.Fatal(err)
	}
	org := &semantics.Organization{Schema: semantics.OrganizationSchema,
		Units: []semantics.SemanticUnit{{ID: "lunch", Selector: domain.TextSelector{Exact: "Lunch includes walnut pie."}}},
		Links: []semantics.GroundedLink{{Type: "applies_in", Meaning: "The lunch item has a storage condition", Source: semantics.GraphEndpoint{AssetID: seed.Information.ID, UnitID: "lunch"}, Target: semantics.GraphEndpoint{AssetID: neighbor.Information.ID, Selector: domain.TextSelector{Exact: neighbor.Information.Content}}}},
	}
	if _, err := service.SubmitSemantic(ctx, semanticSubmission(work[0], semantics.Analysis{Organization: org})); err != nil {
		t.Fatal(err)
	}
	// Make the multi-topic source the vector seed; the maintenance source is
	// direct lexical evidence and the food condition is only a graph neighbor.
	queryVector, _ := vector.EmbedQuery(ctx, "Lumen gateway")
	for _, id := range []string{seed.Information.ID, direct.Information.ID, neighbor.Information.ID} {
		record, _ := store.Get(id)
		record.Embedding = nil
		if id == seed.Information.ID {
			record.Embedding = queryVector
		}
		service.semantic.Upsert(record)
	}
	hits, err := service.Search(ctx, SearchInput{Query: "Lumen gateway", Limit: 2})
	if err != nil || len(hits) != 2 {
		t.Fatalf("search failed: %#v %v", hits, err)
	}
	found := false
	for _, hit := range hits {
		if hit.ID == direct.Information.ID {
			found = true
		}
		if hit.ID == neighbor.Information.ID || len(hit.Relations) != 0 {
			t.Fatalf("unrelated source topic displaced query evidence: %#v", hits)
		}
	}
	if !found {
		t.Fatalf("direct evidence was displaced: %#v", hits)
	}
}

func TestSnapshotChangeReissuesOnlyAffectedWorkWithoutDerivedFeedback(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := derived.Open(filepath.Join(root, "derived"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := newTestCollaborative(t, assets, store, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	a, err := service.Create(ctx, CreateInput{Content: "Beacon is permitted only indoors."})
	if err != nil {
		t.Fatal(err)
	}
	w, err := service.SemanticWorkFor(ctx, []string{a.Information.ID})
	if err != nil {
		t.Fatal(err)
	}
	org := func(id string) *semantics.Organization {
		return &semantics.Organization{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{{ID: id}}}
	}
	if _, err = service.SubmitSemantic(ctx, semanticSubmission(w[0], semantics.Analysis{Summary: "Beacon condition", Organization: org("u")})); err != nil {
		t.Fatal(err)
	}
	b, err := service.Create(ctx, CreateInput{Content: "Lumen gateway uses Beacon."})
	if err != nil {
		t.Fatal(err)
	}
	w, err = service.SemanticWorkFor(ctx, []string{b.Information.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(w[0].Candidates) == 0 || w[0].Candidates[0].Organization == nil {
		t.Fatal("candidate inventory not delivered")
	}
	old := semanticSubmission(w[0], semantics.Analysis{Summary: "Lumen uses Beacon", Organization: org("b")})
	if _, err = service.SubmitSemantic(ctx, old); err != nil {
		t.Fatal(err)
	}
	aRecord, _ := store.GetWithEmbedding(a.Information.ID)
	service.prepareSemanticWorkWithVector(a.Information, aRecord.Embedding, nil)
	w, err = service.SemanticWorkFor(ctx, []string{a.Information.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range w[0].Candidates {
		if c.Organization != nil {
			t.Fatal("derived dependency cycle introduced")
		}
	}
	if _, err = service.SubmitSemantic(ctx, semanticSubmission(w[0], semantics.Analysis{Summary: "Beacon condition", Organization: org("new-unit")})); err != nil {
		t.Fatal(err)
	}
	current, _ := store.Get(b.Information.ID)
	if service.organizationCurrent(current) {
		t.Fatal("stale generated interpretation still visible")
	}
	if _, err := service.SubmitSemantic(ctx, old); err == nil {
		t.Fatal("late old work accepted")
	}
	w, err = service.SemanticWorkFor(ctx, []string{b.Information.ID})
	if err != nil || len(w) != 1 {
		t.Fatalf("automatic pickup failed: %#v %v", w, err)
	}
	if w[0].ID == old.WorkID || w[0].Previous != nil {
		t.Fatal("recovery reused superseded work or interpretation")
	}
	for _, c := range w[0].Candidates {
		if c.Organization != nil {
			t.Fatal("recovery depends on old interpretations")
		}
	}
	if _, err = service.SubmitSemantic(ctx, semanticSubmission(w[0], semantics.Analysis{Summary: "Lumen uses Beacon", Organization: org("b")})); err != nil {
		t.Fatal(err)
	}
	w, err = service.SemanticWorkFor(ctx, []string{a.Information.ID, b.Information.ID})
	if err != nil || len(w) != 0 {
		t.Fatalf("recovery did not settle: %#v %v", w, err)
	}
	read, err := service.Read(ctx, a.Information.ID)
	if err != nil || read.Content != a.Information.Content {
		t.Fatal("derived repair changed original source")
	}
}
func TestBatchInputBeyondOriginalCandidatesRemainsARealDependency(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := derived.Open(filepath.Join(root, "derived"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := newTestCollaborative(t, assets, store, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	a, err := service.Create(ctx, CreateInput{Content: "Lumen uses Beacon."})
	if err != nil {
		t.Fatal(err)
	}
	w, err := service.SemanticWorkFor(ctx, []string{a.Information.ID})
	if err != nil {
		t.Fatal(err)
	}
	// This source is created after A's work was issued, then provided by the host
	// in the same model call. It is an input even if no relation is returned.
	b, err := service.Create(ctx, CreateInput{Content: "Beacon requires indoor use."})
	if err != nil {
		t.Fatal(err)
	}
	submission := semanticSubmission(w[0], semantics.Analysis{Summary: "Lumen uses Beacon", Organization: &semantics.Organization{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{{ID: "u"}}}})
	submission.InputAssets = []semantics.CandidateReference{{ID: b.Information.ID, Revision: 1}}
	if _, err = service.SubmitSemantic(ctx, submission); err != nil {
		t.Fatal(err)
	}
	record, _ := store.Get(a.Information.ID)
	found := false
	for _, ref := range derived.Inputs(record) {
		found = found || ref.ID == b.Information.ID
	}
	if !found {
		t.Fatal("batch-only input lost")
	}
	changed := "Beacon is no longer permitted."
	if _, err = service.Update(ctx, UpdateInput{ID: b.Information.ID, ExpectedRevision: 1, Content: &changed}); err != nil {
		t.Fatal(err)
	}
	record, _ = store.Get(a.Information.ID)
	if record.Analysis.Organization != nil && service.organizationCurrent(record) {
		t.Fatal("no-edge input change did not invalidate interpretation")
	}
	if _, err = service.SubmitSemantic(ctx, submission); err == nil {
		t.Fatal("late batch input accepted")
	}
}
