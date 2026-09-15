package core

import (
	"context"
	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceObjectReadNavigateRecovery(t *testing.T) {
	var value struct {
		Work     semantics.Work
		Analysis struct {
			semantics.Analysis
			InputAssets []semantics.CandidateReference
		}
	}
	value.Work.Asset = domain.Information{ID: "a", Revision: 1, Content: "Luna is our dog.\nKeep her indoors."}
	value.Work.Candidates = []semantics.Candidate{{ID: "b", Revision: 1, Content: "My sister calls our dog Moon."}}
	value.Analysis.Organization = &semantics.Organization{Schema: semantics.OrganizationSchema,
		Units: []semantics.SemanticUnit{{ID: "u", Selector: domain.TextSelector{Exact: "Luna is our dog."},
			Context: []domain.TextSelector{{Exact: "Keep her indoors."}}, Mentions: []semantics.Mention{{ID: "m", Name: "Luna", Selector: domain.TextSelector{Exact: "Luna"}}}}},
		Links: []semantics.GroundedLink{{Type: "same_object", Meaning: "Two names for the same pet",
			Source: semantics.GraphEndpoint{AssetID: "a", UnitID: "u", MentionID: "m"},
			Target: semantics.GraphEndpoint{AssetID: "b", ObjectName: "Moon", Selector: domain.TextSelector{Exact: value.Work.Candidates[0].Content}}}}}
	value.Analysis.InputAssets = []semantics.CandidateReference{{ID: "a", Revision: 1}, {ID: "b", Revision: 1}}
	var err error
	ctx := context.Background()
	root := t.TempDir()
	open := func() *ownedTestService {
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
		t.Cleanup(func() { _ = service.Close() })
		return service
	}
	service := open()
	ids := map[string]string{}
	create := func(id, content string, contexts []domain.Context) {
		r, e := service.Create(ctx, CreateInput{Content: content, Contexts: contexts})
		if e != nil {
			t.Fatal(e)
		}
		ids[id] = r.Information.ID
	}
	create(value.Work.Asset.ID, value.Work.Asset.Content, value.Work.Asset.Contexts)
	for _, c := range value.Work.Candidates {
		create(c.ID, c.Content, c.Contexts)
	}
	targetID := ids[value.Work.Asset.ID]
	work, err := service.SemanticWorkFor(ctx, []string{targetID})
	if err != nil || len(work) != 1 {
		t.Fatal(err)
	}
	for _, c := range work[0].Candidates {
		if c.Organization != nil {
			t.Fatal("foreign organization was invented")
		}
	}
	org := value.Analysis.Organization
	originalTarget := org.Links[0].Target.AssetID
	expectedEvidence := org.Links[0].Target.Selector.Exact
	for n := range org.Links {
		endpoints := []*semantics.GraphEndpoint{&org.Links[n].Source, &org.Links[n].Target}
		for c := range org.Links[n].Conditions {
			endpoints = append(endpoints, &org.Links[n].Conditions[c])
		}
		for _, e := range endpoints {
			bound, ok := ids[e.AssetID]
			if !ok {
				t.Fatalf("unprovided original endpoint %s", e.AssetID)
			}
			e.AssetID = bound
		}
	}
	sub := semanticSubmission(work[0], value.Analysis.Analysis)
	for _, ref := range value.Analysis.InputAssets {
		bound, ok := ids[ref.ID]
		if !ok {
			t.Fatal("lost original input provenance")
		}
		ref.ID = bound
		sub.InputAssets = append(sub.InputAssets, ref)
	}
	if len(sub.InputAssets) != 2 {
		t.Fatalf("expected both original inputs, got %d", len(sub.InputAssets))
	}
	if _, err = service.SubmitSemantic(ctx, sub); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SubmitSemantic(ctx, sub); err != nil {
		t.Fatalf("same submission not idempotent: %v", err)
	}
	legacy, err := service.Create(ctx, CreateInput{Content: "Cobalt stays indoors."})
	if err != nil {
		t.Fatal(err)
	}
	legacyWork, err := service.SemanticWorkFor(ctx, []string{legacy.Information.ID})
	if err != nil || len(legacyWork) != 1 {
		t.Fatal(err)
	}
	legacyOrg := &semantics.Organization{Schema: semantics.LegacyOrganizationSchema,
		Units: []semantics.SemanticUnit{{ID: "legacy", Selector: domain.TextSelector{Exact: legacy.Information.Content},
			Mentions: []semantics.Mention{{ID: "m", Name: "Cobalt", Selector: domain.TextSelector{Exact: "Cobalt"}}}}}}
	if _, err = service.SubmitSemantic(ctx, semanticSubmission(legacyWork[0], semantics.Analysis{Organization: legacyOrg})); err != nil {
		t.Fatal(err)
	}
	check := func() {
		legacyNames := service.semantic.NameSearch("Cobalt", 10)
		if len(legacyNames) != 1 || legacyNames[0].AssetID != legacy.Information.ID {
			t.Fatal("legacy object index lost while reading v2 records")
		}
		nav, err := service.Navigate(ctx, []string{targetID}, []string{"same_object"}, 1, 10)
		if err != nil || len(nav.Edges) != 1 || nav.Edges[0].Grounded == nil {
			t.Fatalf("identity navigation failed: %v %#v", err, nav)
		}
		link := nav.Edges[0].Grounded
		if link.Type != "same_object" || !strings.Contains(link.Meaning, "same pet") {
			t.Fatal("identity meaning changed")
		}
		evidence, err := service.ReadEvidence(ctx, link.Target.ID)
		if err != nil || evidence.Content != expectedEvidence {
			t.Fatalf("foreign original evidence changed: %v", err)
		}
		if len(link.Context) == 0 {
			t.Fatal("source unit context lost")
		}
		for _, ref := range link.Context {
			if _, err := service.ReadEvidence(ctx, ref.ID); err != nil {
				t.Fatal(err)
			}
		}
		reverse, err := service.Navigate(ctx, []string{ids[originalTarget]}, []string{"same_object"}, 1, 10)
		if err != nil || len(reverse.Edges) != 1 {
			t.Fatal("reverse identity navigation lost")
		}
		wrong, err := service.Navigate(ctx, []string{targetID}, []string{"same_as"}, 1, 10)
		if err != nil || len(wrong.Edges) != 0 {
			t.Fatal("object identity leaked into statement equivalence")
		}
	}
	check()
	if err = service.Close(); err != nil {
		t.Fatal(err)
	}
	service = open()
	defer service.Close()
	check()
	changed := "This source was changed after the object relation was generated."
	if _, err = service.Update(ctx, UpdateInput{ID: ids[originalTarget], ExpectedRevision: 1, Content: &changed}); err != nil {
		t.Fatal(err)
	}
	nav, err := service.Navigate(ctx, []string{targetID}, []string{"same_object"}, 1, 10)
	if err != nil || len(nav.Edges) != 0 {
		t.Fatal("stale source object remained navigable")
	}
	if _, err = service.SubmitSemantic(ctx, sub); err == nil {
		t.Fatal("late old submission accepted")
	}
}

func TestSourceObjectNameIndexesOnlyItsOriginalSource(t *testing.T) {
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
	a, err := service.Create(ctx, CreateInput{Content: "Avery calls the adopted dog Luna."})
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.Create(ctx, CreateInput{Content: "The same adopted dog is called Moon by Avery's sister."})
	if err != nil {
		t.Fatal(err)
	}
	works, err := service.SemanticWorkFor(ctx, []string{a.Information.ID})
	if err != nil || len(works) != 1 {
		t.Fatal(err)
	}
	org := &semantics.Organization{Schema: semantics.OrganizationSchema, Links: []semantics.GroundedLink{{
		Type: "same_object", Meaning: "The owners use different names for the same adopted dog, not the same statement or event.",
		Source: semantics.GraphEndpoint{AssetID: a.Information.ID, ObjectName: "Luna", ObjectRole: "adopted dog", Selector: domain.TextSelector{Exact: a.Information.Content}},
		Target: semantics.GraphEndpoint{AssetID: b.Information.ID, ObjectName: "Moon", ObjectRole: "adopted dog", Selector: domain.TextSelector{Exact: b.Information.Content}},
	}}}
	submission := semanticSubmission(works[0], semantics.Analysis{Organization: org})
	submission.InputAssets = []semantics.CandidateReference{{ID: a.Information.ID, Revision: 1}, {ID: b.Information.ID, Revision: 1}}
	if _, err = service.SubmitSemantic(ctx, submission); err != nil {
		t.Fatal(err)
	}
	names := service.semantic.NameSearch("Luna", 10)
	if len(names) != 1 || names[0].AssetID != a.Information.ID {
		t.Fatal("own source-level object name not indexed")
	}
	if names = service.semantic.NameSearch("Moon", 10); len(names) != 0 {
		t.Fatal("foreign object name was attributed to the owner")
	}
	hits, err := service.Search(ctx, SearchInput{Query: "Luna", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range hits {
		if hit.ID == a.Information.ID {
			found = true
			if contains(hit.Signals, "object") {
				t.Fatal("literal name was counted as independent evidence twice")
			}
		}
	}
	if !found {
		t.Fatal("ordinary search lost the original source")
	}
	nav, err := service.Navigate(ctx, []string{a.Information.ID}, []string{"same_object"}, 1, 5)
	if err != nil || len(nav.Edges) != 1 {
		t.Fatal("source-level aliases were not linked")
	}
	evidence, err := service.ReadEvidence(ctx, nav.Edges[0].Grounded.Target.ID)
	if err != nil || evidence.Content != b.Information.Content {
		t.Fatal("alias evidence lost original statement")
	}
}
