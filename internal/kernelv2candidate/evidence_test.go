package kernelv2candidate

import (
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
)

func TestBoundaryFactsRemainCompleteWithinExistingReadLimit(t *testing.T) {
	fields := []struct{ label, value string }{
		{"orchard vessel", "Lark"},
		{"entry channel", "Silver Narrows"},
		{"departure weekday", "Tuesday"},
		{"sampling depth", "31 meters"},
		{"archive marker", "Cedar-24"},
	}
	var content strings.Builder
	for index, field := range fields {
		content.WriteString("User: ")
		content.WriteString(strings.Repeat(string(rune('a'+index)), 250))
		content.WriteString(" The selected ")
		content.WriteString(field.label)
		content.WriteString(" is ")
		content.WriteString(field.value)
		content.WriteString(". ")
		content.WriteString(strings.Repeat(string(rune('k'+index)), 110))
		content.WriteString("\n\n")
	}
	asset := domain.Information{Schema: domain.AssetSchema, ID: "boundary-facts", Revision: 1, Kind: domain.KindGeneral, Content: content.String()}
	references := RankEvidence(asset, "chosen orchard vessel entry channel departure weekday sampling depth archive marker", 3)
	if len(references) != 3 {
		t.Fatalf("unexpected reference count: %d", len(references))
	}
	var delivered strings.Builder
	for _, reference := range references {
		if reference.ContentRunes > derived.DefaultEvidenceUnitRunes+PredecessorRunes+SuccessorRunes {
			t.Fatalf("continuity range exceeded its bound: %#v", reference)
		}
		unit, err := derived.ParseEvidenceUnitID(reference.ID)
		if err != nil {
			t.Fatal(err)
		}
		evidence, err := derived.ResolveEvidence(asset, unit)
		if err != nil {
			t.Fatal(err)
		}
		delivered.WriteString(evidence.Content)
	}
	for _, field := range fields {
		if !strings.Contains(delivered.String(), field.label+" is "+field.value) {
			t.Errorf("missing boundary fact %q from %q", field.label, delivered.String())
		}
	}
}

func TestCurrentTemporalFactStillOutranksStaleFact(t *testing.T) {
	stale := "The previous harbor assignment was Umber Pier on Monday."
	current := "The superseding harbor assignment is Cobalt Quay on Thursday."
	content := strings.Repeat("x", 350) + stale + strings.Repeat("y", 500) + current
	asset := domain.Information{Schema: domain.AssetSchema, ID: "temporal", Revision: 1, Kind: domain.KindGeneral, Content: content}
	references := RankEvidence(asset, "superseding harbor assignment weekday", 1)
	if len(references) != 1 {
		t.Fatalf("expected one result, got %d", len(references))
	}
	unit, err := derived.ParseEvidenceUnitID(references[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := derived.ResolveEvidence(asset, unit)
	if err != nil || !strings.Contains(evidence.Content, current) {
		t.Fatalf("current fact was not selected: %#v %v", evidence, err)
	}
}

func BenchmarkRankEvidenceAtFormalMaximumLength(b *testing.B) {
	content := strings.Repeat("neutral context without the requested code. ", 2000) + "superseding archive marker is Indigo-88."
	asset := domain.Information{Schema: domain.AssetSchema, ID: "maximum", Revision: 1, Kind: domain.KindGeneral, Content: content}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if len(RankEvidence(asset, "superseding archive marker", 8)) == 0 {
			b.Fatal("target evidence missing")
		}
	}
}
