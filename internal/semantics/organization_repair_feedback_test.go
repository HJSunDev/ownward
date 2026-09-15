package semantics

import (
	"github.com/HJSunDev/ownward/internal/domain"
	"strings"
	"testing"
)

func TestOrganizationReportsAllIndependentMentionFailures(t *testing.T) {
	asset := domain.Information{ID: "a", Revision: 1, Content: "Alice and Bob.\nTheir plan is ready."}
	org := &Organization{Schema: OrganizationSchema, Units: []SemanticUnit{{ID: "plan", Selector: domain.TextSelector{Exact: "Their plan is ready."}, Mentions: []Mention{
		{ID: "alice", Name: "Alice", Selector: domain.TextSelector{Exact: "Alice"}},
		{ID: "bob", Name: "Bob", Selector: domain.TextSelector{Exact: "Bob"}},
	}}}}
	_, err := NormalizeOrganization(asset, org, nil)
	if err == nil || !strings.Contains(err.Error(), `"alice"`) || !strings.Contains(err.Error(), `"bob"`) {
		t.Fatalf("incomplete feedback: %v", err)
	}
	if len(org.Units[0].Context) != 0 {
		t.Fatal("rejection modified caller organization")
	}
	org.Units[0].Context = []domain.TextSelector{{Exact: "Alice and Bob."}}
	if _, err = NormalizeOrganization(asset, org, nil); err != nil {
		t.Fatalf("same evidence with explicit context rejected: %v", err)
	}
}

func TestMentionSelectorFailureDoesNotHideOtherFailures(t *testing.T) {
	asset := domain.Information{ID: "a", Revision: 1, Content: "Alice and Bob.\nThe plan."}
	org := &Organization{Schema: OrganizationSchema, Units: []SemanticUnit{{ID: "plan", Selector: domain.TextSelector{Exact: "The plan."}, Mentions: []Mention{
		{ID: "missing", Name: "Absent", Selector: domain.TextSelector{Exact: "Absent"}},
		{ID: "bob", Name: "Bob", Selector: domain.TextSelector{Exact: "Bob"}},
	}}}}
	_, err := NormalizeOrganization(asset, org, nil)
	if err == nil || !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), `"bob"`) {
		t.Fatalf("incomplete feedback: %v", err)
	}
}

func TestUnitFailureDoesNotHideIndependentRelationFailure(t *testing.T) {
	asset := domain.Information{ID: "a", Revision: 1, Content: "Alice. The plan."}
	endpoint := GraphEndpoint{AssetID: "a", UnitID: "plan", MentionID: "alice"}
	org := &Organization{Schema: OrganizationSchema,
		Units: []SemanticUnit{{ID: "plan", Selector: domain.TextSelector{Exact: "The plan."},
			Mentions: []Mention{{ID: "alice", Name: "Alice", Selector: domain.TextSelector{Exact: "Alice"}}}}},
		Links: []GroundedLink{{Type: "same_object", Meaning: "same person", Source: endpoint, Target: endpoint}}}
	value, err := NormalizeOrganization(asset, org, nil)
	if value != nil || err == nil || !strings.Contains(err.Error(), "units[0]") || !strings.Contains(err.Error(), "links[0]") {
		t.Fatalf("independent failures were hidden: %v", err)
	}
}
