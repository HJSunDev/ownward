package derived

import (
	"fmt"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
	"testing"
)

func TestOrganizationRebindsOnlyIdenticalUnitsAndInvalidatesGeneratedInputs(t *testing.T) {
	unit := semantics.SemanticUnit{ID: "u", Statement: "original condition", Selector: domain.TextSelector{Exact: "only indoors"}}
	target := Record{AssetID: "target", AssetRevision: 1, Status: "ready", Analysis: semantics.Analysis{Organization: &semantics.Organization{Schema: semantics.OrganizationSchema, Snapshot: "s1", Units: []semantics.SemanticUnit{unit}}}}
	link := semantics.GroundedLink{ID: "l", Type: "applies_in", Meaning: "condition", Source: semantics.GraphEndpoint{AssetID: "owner", Revision: 1, Selector: domain.TextSelector{Exact: "method"}}, Target: semantics.GraphEndpoint{AssetID: "target", Revision: 1, Snapshot: "s1", UnitID: "u", Fingerprint: semantics.UnitFingerprint(unit), Selector: unit.Selector}}
	owner := Record{AssetID: "owner", AssetRevision: 1, Status: "ready", InputAssets: []semantics.CandidateReference{{ID: "target", Revision: 1}}, Analysis: semantics.Analysis{Organization: &semantics.Organization{Schema: semantics.OrganizationSchema, Snapshot: "o1", Links: []semantics.GroundedLink{link}}}}
	index := NewIndex([]Record{target, owner})
	if len(index.Navigate([]string{"owner"}, nil, 1, 10)) != 1 {
		t.Fatal("initial relation missing")
	}
	changed := clone(target)
	changed.Analysis.Organization.Snapshot = "s2"
	changed.Analysis.Organization.Units[0].ID = "renamed"
	index.Upsert(changed)
	edges := index.Navigate([]string{"owner"}, nil, 1, 10)
	if len(edges) != 1 || edges[0].Grounded.Target.UnitID != "renamed" || edges[0].Grounded.Target.Snapshot != "s2" {
		t.Fatal("identical unit did not rebind")
	}
	changed.Analysis.Organization.Units[0].Statement = "different condition"
	changed.Analysis.Organization.Snapshot = "s3"
	index.Upsert(changed)
	if len(index.Navigate([]string{"owner"}, nil, 1, 10)) != 0 {
		t.Fatal("changed semantic unit retained stale reference")
	}
	index.Upsert(target)
	owner.InputAssets[0].OrganizationSnapshot = "s1"
	index.Upsert(owner)
	changed = clone(target)
	changed.Analysis.Organization.Snapshot = "s2"
	index.Upsert(changed)
	if index.OrganizationCurrent("owner") || len(index.Navigate([]string{"owner"}, nil, 1, 10)) != 0 {
		t.Fatal("generated input dependency was repaired by relabeling")
	}
}

func TestNavigationBoundsExaminedEdgesAndResumes(t *testing.T) {
	records := []Record{{AssetID: "hub", AssetRevision: 1, Status: "ready"}}
	for n := 0; n < 800; n++ {
		records = append(records, Record{AssetID: fmt.Sprintf("leaf-%04d", n), AssetRevision: 1, Status: "ready", Analysis: semantics.Analysis{Relations: []semantics.Relation{{TargetID: "hub", Type: "related_to", Confidence: 1}}}})
	}
	index := NewIndex(records)
	filtered, err := index.NavigatePage([]string{"hub"}, []string{"contradicts"}, 1, 10)
	if err != nil || len(filtered.Edges) != 0 || filtered.Examined != navigationWorkBudget || filtered.Continuation == "" {
		t.Fatalf("unbounded filtered scan: %#v %v", filtered, err)
	}
	last, err := index.NavigatePage([]string{filtered.Continuation}, nil, 1, 10)
	if err != nil || last.Examined > navigationWorkBudget || last.Continuation != "" {
		t.Fatalf("filtered scan did not finish: %#v %v", last, err)
	}
	seen := map[string]bool{}
	start := []string{"hub"}
	for n := 0; n < 20; n++ {
		page, err := index.NavigatePage(start, nil, 1, 100)
		if err != nil {
			t.Fatal(err)
		}
		if page.Examined > navigationWorkBudget {
			t.Fatal("budget exceeded")
		}
		for _, edge := range page.Edges {
			if seen[edge.SourceID] {
				t.Fatal("duplicate page edge")
			}
			seen[edge.SourceID] = true
		}
		if page.Continuation == "" {
			break
		}
		start = []string{page.Continuation}
	}
	if len(seen) != 800 {
		t.Fatalf("pagination lost neighbors: %d", len(seen))
	}
	index.Upsert(Record{AssetID: "hub", AssetRevision: 2, Status: "ready"})
	if _, err := index.NavigatePage([]string{filtered.Continuation}, nil, 1, 10); err == nil {
		t.Fatal("stale cursor survived source update")
	}
}
func TestLargeHubContinuationKeepsStateBounded(t *testing.T) {
	records := []Record{{AssetID: "hub", AssetRevision: 1, Status: "ready"}}
	for n := 0; n < 5000; n++ {
		records = append(records, Record{AssetID: fmt.Sprintf("leaf-%04d", n), AssetRevision: 1, Status: "ready", Analysis: semantics.Analysis{Relations: []semantics.Relation{{TargetID: "hub", Type: "related_to", Confidence: 1}}}})
	}
	index := NewIndex(records)
	start := []string{"hub"}
	count := 0
	for n := 0; n < 60; n++ {
		page, err := index.NavigatePage(start, nil, 1, 100)
		if err != nil {
			t.Fatal(err)
		}
		if page.Examined > navigationWorkBudget || len(page.Continuation) > 2048 {
			t.Fatal("unbounded navigation work or state")
		}
		count += len(page.Edges)
		if page.Continuation == "" {
			break
		}
		start = []string{page.Continuation}
	}
	if count != 5000 {
		t.Fatalf("lost large-hub results: %d", count)
	}
}
