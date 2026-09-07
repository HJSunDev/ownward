package kernelv2candidate

import (
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
)

func TestCueContextPreservesTheFollowingSentence(t *testing.T) {
	for _, ending := range []string{"48 kHz mono PCM WAV.", "每份记录保留原始编号与校验码。"} {
		cue := "User: What delivery format is required for the archive?"
		answer := "Assistant: The approved format for the delivered copy must be " + ending
		asset := domain.Information{ID: "cue-boundary", Revision: 1,
			Content: strings.Repeat("Unrelated background. ", 30) + cue + "\n\n" + answer + "\n\nOther: unrelated follow-up."}
		refs := RankEvidenceWithCues(asset, "delivery format archive", 1, []string{cue})
		if len(refs) != 1 {
			t.Fatal("missing reference")
		}
		unit, err := derived.ParseEvidenceUnitID(refs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		evidence, err := derived.ResolveEvidence(asset, unit)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(evidence.Content, answer) {
			t.Fatalf("following answer was cut off: %q", evidence.Content)
		}
	}
}

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

func TestProbeEvidencePreservesFirstRankAndReportsDepth(t *testing.T) {
	content := strings.Repeat("neutral preface. ", 40) +
		"The current harbor marker is Cobalt-41. " + strings.Repeat("neutral bridge. ", 40) +
		"The related departure channel is Silver Narrows."
	asset := domain.Information{Schema: domain.AssetSchema, ID: "probe", Revision: 3, Kind: domain.KindGeneral, Content: content}
	full := RankEvidence(asset, "harbor marker departure channel", 2)
	probe, deep := ProbeEvidence(asset, "harbor marker departure channel")
	if len(full) != 2 || len(probe) != 1 || !deep {
		t.Fatalf("probe must expose the first reference and preserve the deep-lane fact: full=%v probe=%v deep=%v", full, probe, deep)
	}
	if probe[0] != full[0] {
		t.Fatalf("probe changed the first ranked reference: full=%v probe=%v", full[0], probe[0])
	}
}

func TestGroundedCuesDeliverDistinctFactsAheadOfTopicBoilerplate(t *testing.T) {
	first := "User: We reserved five rooms at Cedar Hall for the archive group."
	second := "Assistant: The later archive group reservation covers seven rooms at Birch Hall."
	content := strings.Repeat("Archive group reservation advice: compare rooms and halls. ", 60) + first +
		strings.Repeat("Background material without any booking decisions. ", 25) + second
	asset := domain.Information{ID: "cue-evidence", Revision: 2, Content: content}
	refs := RankEvidenceWithCues(asset, "archive group rooms reservation", 2,
		[]string{first, second, "The archive group booked ninety rooms in a different source."})
	var delivered strings.Builder
	for _, ref := range refs {
		unit, err := derived.ParseEvidenceUnitID(ref.ID)
		if err != nil {
			t.Fatal(err)
		}
		value, err := derived.ResolveEvidence(asset, unit)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(value.Content, strings.Trim(ref.Preview, "…")) {
			t.Fatal("preview escaped source range")
		}
		delivered.WriteString(value.Content)
	}
	if !strings.Contains(delivered.String(), first) || !strings.Contains(delivered.String(), second) || strings.Contains(delivered.String(), "ninety") {
		t.Fatalf("distinct source-owned facts were lost: %s", delivered.String())
	}
}

func TestSearchEvidencePreservesRawFactsMissingFromSemanticMetadata(t *testing.T) {
	fact := "The archive delivery arrived on Thursday at Cedar Hall."
	cue := "A future archive delivery might use the west entrance."
	asset := domain.Information{ID: "partial-metadata", Revision: 1,
		Content: fact + strings.Repeat(" Unrelated background material.", 50) + cue}
	refs := SearchEvidence(asset, "archive delivery arrived Thursday", []string{cue})
	if len(refs) == 0 || len(refs) > 3 {
		t.Fatalf("unbounded search evidence: %d", len(refs))
	}
	found := false
	for _, ref := range refs {
		unit, err := derived.ParseEvidenceUnitID(ref.ID)
		if err != nil {
			t.Fatal(err)
		}
		evidence, err := derived.ResolveEvidence(asset, unit)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(evidence.Content, fact) {
			found = true
		}
	}
	if !found {
		t.Fatal("incomplete semantic metadata hid the original fact")
	}
}

func TestStoredShortenedCueAnchorsOriginalFactWithoutImportingMetadata(t *testing.T) {
	quote := "The archive delivery from the northern warehouse arrived at the west entrance after the staff meeting; the receipt records seven crates."
	shortened := string([]rune(quote)[:128]) + "…"
	asset := domain.Information{ID: "shortened-cue", Revision: 2,
		Content: strings.Repeat("Archive delivery receipt advice and entrance planning. ", 40) + "\n" + quote}
	refs := SearchEvidence(asset, "archive delivery receipt crates", []string{shortened})
	found := false
	for _, ref := range refs {
		if strings.Contains(ref.Preview, "seven crates") {
			found = true
		}
	}
	if !found {
		t.Fatal("stored display truncation hid an original fact")
	}
	if cues := queryCues(asset.Content, "archive", []string{"The archive imported forty crates from a different source…"}); len(cues) != 0 {
		t.Fatal("an ungrounded shortened quote crossed its source boundary")
	}
	if cues := queryCues(quote+"\n"+quote, "archive", []string{shortened}); len(cues) != 0 {
		t.Fatal("an ambiguous shortened quote was assigned to an arbitrary location")
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
