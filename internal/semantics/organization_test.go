package semantics

import (
	"github.com/HJSunDev/ownward/internal/domain"
	"strings"
	"testing"
)

func TestOrganizationReportsIndependentReferenceErrorsTogether(t *testing.T) {
	asset := domain.Information{ID: "a", Revision: 1, Content: "Mara opened the lab. Mara left the studio."}
	value := &Organization{Schema: OrganizationSchema, Units: []SemanticUnit{
		{ID: "lab", Selector: domain.TextSelector{Exact: "Mara opened the lab."}},
		{ID: "studio", Selector: domain.TextSelector{Exact: "Mara left the studio."}},
	}, Links: []GroundedLink{
		{Type: "related_to", Meaning: "activities", Source: GraphEndpoint{AssetID: "a", UnitID: "lab", MentionID: "missing-person"}, Target: GraphEndpoint{AssetID: "a", UnitID: "studio"}},
		{Type: "related_to", Meaning: "activities", Source: GraphEndpoint{AssetID: "a", UnitID: "lab"}, Target: GraphEndpoint{AssetID: "a", UnitID: "missing-unit"}},
	}}
	_, err := NormalizeOrganization(asset, value, nil)
	if err == nil {
		t.Fatal("invalid references accepted")
	}
	for _, fragment := range []string{"links[0].source", "missing-person", "links[1].target", "missing-unit", "lab", "studio"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("repair feedback omitted %q: %v", fragment, err)
		}
	}
	value.Links[0].Source.MentionID = ""
	value.Links[1].Target.UnitID = "studio"
	if _, err := NormalizeOrganization(asset, value, nil); err != nil {
		t.Fatalf("valid correction rejected: %v", err)
	}
}

func TestWholeSourceRelationKeepsReferenceInsteadOfDuplicatingBody(t *testing.T) {
	asset := domain.Information{ID: "a", Revision: 1, Content: "Use method Beacon."}
	candidate := Candidate{ID: "b", Revision: 1, Content: "Beacon needs shelter."}
	value := &Organization{Schema: OrganizationSchema, Units: []SemanticUnit{}, Links: []GroundedLink{{
		Type: "applies_in", Meaning: "shelter condition", Source: GraphEndpoint{AssetID: "a", Selector: domain.TextSelector{Exact: asset.Content}},
		Target: GraphEndpoint{AssetID: "b", Selector: domain.TextSelector{Exact: candidate.Content}},
	}}}
	first, err := NormalizeOrganization(asset, value, []Candidate{candidate})
	if err != nil || first.Links[0].Source.Selector.Exact != "" || first.Links[0].Target.Selector.Exact != "" {
		t.Fatalf("whole sources duplicated on relation: %#v %v", first, err)
	}
	value.Links[0].Source.Selector = domain.TextSelector{}
	value.Links[0].Target.Selector = domain.TextSelector{}
	second, err := NormalizeOrganization(asset, value, []Candidate{candidate})
	if err != nil || first.Links[0].ID != second.Links[0].ID {
		t.Fatal("equivalent whole-source references changed relation identity")
	}
}

func TestOrganizationReportsEveryMisplacedMentionUnit(t *testing.T) {
	asset := domain.Information{ID: "a", Revision: 1, Content: "Mara opened the lab. Jan left the studio."}
	value := &Organization{Schema: OrganizationSchema, Units: []SemanticUnit{
		{ID: "lab", Selector: domain.TextSelector{Exact: "Mara opened the lab."}, Mentions: []Mention{{ID: "jan", Name: "Jan", Selector: domain.TextSelector{Exact: "Jan"}}}},
		{ID: "studio", Selector: domain.TextSelector{Exact: "Jan left the studio."}, Mentions: []Mention{{ID: "mara", Name: "Mara", Selector: domain.TextSelector{Exact: "Mara"}}}},
	}}
	_, err := NormalizeOrganization(asset, value, nil)
	if err == nil || !strings.Contains(err.Error(), "units[0]") || !strings.Contains(err.Error(), "units[1]") {
		t.Fatalf("source repair needs all incorrect unit locations: %v", err)
	}
}

func TestOrganizationKeepsSourceBoundariesAndStableSnapshot(t *testing.T) {
	asset := domain.Information{ID: "a", Revision: 1, Content: "Mara opened the lab. Mara left the studio."}
	unit := SemanticUnit{ID: "u", Statement: "Mara opened the lab", Selector: domain.TextSelector{Exact: "Mara opened the lab."}, Mentions: []Mention{{ID: "m", Name: "Mara", Role: "opener", Selector: domain.TextSelector{Exact: "Mara", Suffix: " opened"}}}}
	original := &Organization{Schema: OrganizationSchema, Units: []SemanticUnit{unit}}
	first, err := NormalizeOrganization(asset, original, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = UnitFingerprint(unit)
	if unit.Mentions[0].ID != "m" {
		t.Fatal("fingerprint mutated caller input")
	}
	linked := CloneOrganization(first)
	linked.Links = []GroundedLink{{Type: "related_to", Meaning: "distinct activities by the named person", Source: GraphEndpoint{AssetID: "a", UnitID: "u", Selector: unit.Selector}, Target: GraphEndpoint{AssetID: "a", Selector: domain.TextSelector{Exact: "Mara left the studio."}}}}
	second, err := NormalizeOrganization(asset, linked, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot != second.Snapshot || second.Links[0].Source.Fingerprint == "" {
		t.Fatal("adding a link changed endpoint identity or omitted exact binding")
	}
	bad := CloneOrganization(first)
	bad.Units[0].Mentions[0].Selector = domain.TextSelector{Exact: "Mara", Suffix: " left"}
	if _, err := NormalizeOrganization(asset, bad, nil); err == nil {
		t.Fatal("a mention from another event was accepted")
	}
	bad = CloneOrganization(first)
	bad.Units[0].Selector = domain.TextSelector{Exact: "Mara"}
	if _, err := NormalizeOrganization(asset, bad, nil); err == nil {
		t.Fatal("ambiguous quote was accepted")
	}
	bad = CloneOrganization(second)
	bad.Links[0].Type = "same_object"
	if _, err := NormalizeOrganization(asset, bad, nil); err == nil {
		t.Fatal("statements masqueraded as object identity")
	}
	bad = CloneOrganization(second)
	bad.Links[0].Target.AssetID = "outside"
	if _, err := NormalizeOrganization(asset, bad, nil); err == nil {
		t.Fatal("unseen source was accepted")
	}
}

func TestOrganizationDoesNotDropRelationsWithDifferentConditions(t *testing.T) {
	asset := domain.Information{ID: "a", Revision: 1, Content: "Use protocol R. Only indoors. Outdoors requires shelter."}
	source := GraphEndpoint{AssetID: "a", Selector: domain.TextSelector{Exact: "Use protocol R."}}
	target := GraphEndpoint{AssetID: "a", Selector: domain.TextSelector{Exact: "Only indoors."}}
	original := &Organization{Schema: OrganizationSchema, Units: []SemanticUnit{}, Links: []GroundedLink{
		{Type: "applies_in", Meaning: "indoor use", Source: source, Target: target},
		{Type: "applies_in", Meaning: "sheltered outdoor use", Source: source, Target: target, Conditions: []GraphEndpoint{{AssetID: "a", Selector: domain.TextSelector{Exact: "Outdoors requires shelter."}}}},
	}}
	result, err := NormalizeOrganization(asset, original, nil)
	if err != nil || len(result.Links) != 2 || result.Links[0].ID == result.Links[1].ID {
		t.Fatalf("conditioned links collapsed: %#v %v", result, err)
	}
}

func TestImplicitMentionPreservesOriginalCaseAndRejectsAmbiguity(t *testing.T) {
	asset := domain.Information{ID: "a", Revision: 1, Content: "Mara opened the lab. Mara left the studio."}
	org := &Organization{Schema: OrganizationSchema, Units: []SemanticUnit{{ID: "u", Selector: domain.TextSelector{Exact: "Mara opened the lab."}, Mentions: []Mention{{ID: "m", Name: "mara"}}}}}
	value, err := NormalizeOrganization(asset, org, nil)
	if err != nil || value.Units[0].Mentions[0].Selector.Exact != "Mara" {
		t.Fatalf("literal name resolution failed: %#v %v", value, err)
	}
	org.Units[0].Selector = domain.TextSelector{}
	if _, err := NormalizeOrganization(asset, org, nil); err == nil {
		t.Fatal("ambiguous implicit mention accepted")
	}
	org.Units[0].Mentions[0].Name = "unknown"
	if _, err := NormalizeOrganization(asset, org, nil); err == nil {
		t.Fatal("invented name accepted")
	}
}
