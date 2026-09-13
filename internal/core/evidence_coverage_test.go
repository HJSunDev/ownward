package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestEvidenceSearchDoesNotTreatOrganizationAsCompleteSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := newTestCollaborative(t, assets, store, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	fee := "The archive delivery cost 37 credits including its receipt."
	constraint := "The operator requires a quiet loading bay and no vehicle idling."
	organized := "The archive provides routine delivery planning advice."
	content := "Record timestamp: 2031-07-18\n\n" + strings.Repeat("Archive delivery planning background. ", 30) +
		"\n\n" + fee + "\n\n" + strings.Repeat("Ordinary planning background. ", 30) +
		"\n\n" + constraint + "\n\n" + organized
	created, err := service.Create(ctx, CreateInput{Content: content})
	if err != nil {
		t.Fatal(err)
	}
	work, err := service.SemanticWorkFor(ctx, []string{created.Information.ID})
	if err != nil || len(work) != 1 {
		t.Fatalf("semantic work: %v %v", work, err)
	}
	_, err = service.SubmitSemantic(ctx, semanticSubmission(work[0], semantics.Analysis{
		Summary: organized,
		Organization: &semantics.Organization{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{
			{ID: "planning", Statement: organized, Selector: domain.TextSelector{Exact: organized}},
		}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ query, want string }{
		{"archive record timestamp", "2031-07-18"},
		{"archive delivery cost receipt", fee},
		{"archive operator quiet loading bay vehicle idling", constraint},
	} {
		refs, err := service.SearchEvidence(ctx, EvidenceSearchInput{SourceID: created.Information.ID, Query: test.query, Limit: 3})
		if err != nil || len(refs) > 3 {
			t.Fatalf("bounded evidence lookup: %v %v", refs, err)
		}
		var delivered strings.Builder
		for _, ref := range refs {
			evidence, err := service.ReadEvidence(ctx, ref.ID)
			if err != nil || evidence.Reference() != ref || !strings.Contains(content, evidence.Content) {
				t.Fatalf("evidence is not bound to its source: %v %v", evidence, err)
			}
			delivered.WriteString(evidence.Content)
		}
		if !strings.Contains(delivered.String(), test.want) {
			t.Errorf("incomplete organization hid %q for %q", test.want, test.query)
		}
		hits, err := service.Search(ctx, SearchInput{Query: test.query, Limit: 1})
		if err != nil || len(hits) != 1 || len(hits[0].Evidence) == 0 {
			t.Fatalf("ordinary search lost its raw reading entrance: %v %v", hits, err)
		}
	}
}

func TestRawEvidenceCompletesAdjacentConstraintWithinBound(t *testing.T) {
	content := strings.Repeat("Background. ", 40) + "\n\n" +
		strings.Repeat("Details of the visit. ", 10) +
		"The operator selects the northern loading bay. The vehicle must remain silent.\n\n" +
		strings.Repeat("Unrelated later work. ", 30)
	asset := domain.Information{ID: "bounded-context", Revision: 1, Content: content}
	refs := rankEvidence(asset, "operator northern loading bay", 1)
	if len(refs) != 1 || refs[0].ContentRunes > 3*derived.DefaultEvidenceUnitRunes {
		t.Fatalf("unbounded evidence: %v", refs)
	}
	unit, err := derived.ParseEvidenceUnitID(refs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := derived.ResolveEvidence(asset, unit)
	if err != nil || !strings.Contains(got.Content, "The vehicle must remain silent.") {
		t.Fatalf("adjacent constraint lost: %v %v", got, err)
	}
}
