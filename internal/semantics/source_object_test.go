package semantics

import (
	"testing"

	"github.com/HJSunDev/ownward/internal/domain"
)

func TestSourceObjectEndpointsWithoutPublishedInventory(t *testing.T) {
	a := domain.Information{ID: "a", Revision: 1, Content: "Luna is our dog."}
	b := Candidate{ID: "b", Revision: 2, Content: "My sister calls our dog Moon. Moon stays indoors."}
	org := &Organization{Schema: OrganizationSchema, Links: []GroundedLink{{
		Type: "same_object", Meaning: "Two names for the same dog",
		Source: GraphEndpoint{AssetID: "a", ObjectName: "Luna", Selector: domain.TextSelector{Exact: a.Content}},
		Target: GraphEndpoint{AssetID: "b", ObjectName: "Moon", Selector: domain.TextSelector{Exact: b.Content}},
	}}}
	got, err := NormalizeOrganization(a, org, []Candidate{b})
	if err != nil || got.Links[0].Target.Revision != 2 || got.Links[0].Target.Selector.Exact != b.Content {
		t.Fatalf("grounded cold object rejected or changed: %#v %v", got, err)
	}
	cases := map[string]func(*Organization){
		"empty name":            func(x *Organization) { x.Links[0].Target.ObjectName = " " },
		"missing evidence":      func(x *Organization) { x.Links[0].Target.Selector = domain.TextSelector{} },
		"foreign quote":         func(x *Organization) { x.Links[0].Target.Selector.Exact = "not in the source" },
		"ambiguous quote":       func(x *Organization) { x.Links[0].Target.Selector.Exact = "Moon" },
		"unknown source":        func(x *Organization) { x.Links[0].Target.AssetID = "unknown" },
		"old revision":          func(x *Organization) { x.Links[0].Target.Revision = 1 },
		"invented unit":         func(x *Organization) { x.Links[0].Target.UnitID = "fake" },
		"invented mention":      func(x *Organization) { x.Links[0].Target.MentionID = "fake" },
		"invented snapshot":     func(x *Organization) { x.Links[0].Target.Snapshot = "fake" },
		"legacy schema":         func(x *Organization) { x.Schema = LegacyOrganizationSchema },
		"statement equivalence": func(x *Organization) { x.Links[0].Type = "same_as" },
		"ordinary relation":     func(x *Organization) { x.Links[0].Type = "related_to" },
		"object condition":      func(x *Organization) { x.Links[0].Conditions = []GraphEndpoint{x.Links[0].Target} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			x := CloneOrganization(org)
			mutate(x)
			if _, err := NormalizeOrganization(a, x, []Candidate{b}); err == nil {
				t.Fatal("invalid object endpoint accepted")
			}
		})
	}
	legacy := &Organization{Schema: LegacyOrganizationSchema, Units: []SemanticUnit{{ID: "u", Selector: domain.TextSelector{Exact: a.Content}}}}
	if _, err := NormalizeOrganization(a, legacy, nil); err != nil {
		t.Fatalf("existing v1 data rejected: %v", err)
	}
}
