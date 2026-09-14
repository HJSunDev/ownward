package core

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
)

func TestRankEvidenceFindsMiddleFactAtFormalMaximumLength(t *testing.T) {
	fact := "风铃档案的最大长度记录指出赤陶校验码是八一四。"
	padding := "最大规模的隔离背景条目。"
	remaining := 78_215 - len([]rune(fact))
	content := repeatEvidencePadding(padding, remaining/2) + fact + repeatEvidencePadding(padding, remaining-remaining/2)
	asset := domain.Information{Schema: domain.AssetSchema, ID: "max-source", Revision: 1, Kind: domain.KindGeneral, Content: content}
	references := rankEvidence(asset, "风铃档案的赤陶校验码是什么？", 3)
	if len(references) == 0 {
		t.Fatal("maximum-length source returned no query-specific evidence")
	}
	unit, err := derived.ParseEvidenceUnitID(references[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := derived.ResolveEvidence(asset, unit)
	if err != nil || !strings.Contains(evidence.Content, fact) {
		t.Fatalf("selected evidence missed the middle fact: evidence=%#v err=%v", evidence, err)
	}
}

func BenchmarkRankEvidenceAtFormalMaximumLength(b *testing.B) {
	fact := "风铃档案的最大长度记录指出赤陶校验码是八一四。"
	padding := "最大规模的隔离背景条目。"
	remaining := 78_215 - len([]rune(fact))
	content := repeatEvidencePadding(padding, remaining/2) + fact + repeatEvidencePadding(padding, remaining-remaining/2)
	asset := domain.Information{Schema: domain.AssetSchema, ID: "max-source", Revision: 1, Kind: domain.KindGeneral, Content: content}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if references := rankEvidence(asset, "风铃档案的赤陶校验码是什么？", 3); len(references) == 0 {
			b.Fatal("maximum-length source returned no query-specific evidence")
		}
	}
}

func TestRankEvidencePrefersTargetAmongSimilarFactsInOneLongSource(t *testing.T) {
	target := "极光谱库的松果批次最终核准码是玄青五二。"
	distractor := "极光谱库的松果批次初检码是浅灰一四，复检码是赭石三九，均不是最终核准结果。"
	content := repeatEvidencePadding(distractor, 10_000) + target + repeatEvidencePadding(distractor, 10_000)
	asset := domain.Information{Schema: domain.AssetSchema, ID: "similar-facts", Revision: 1, Kind: domain.KindGeneral, Content: content}
	references := rankEvidence(asset, "极光谱库的松果批次最终核准码是什么？", 3)
	if len(references) == 0 {
		t.Fatal("similar-fact source returned no query-specific evidence")
	}
	unit, err := derived.ParseEvidenceUnitID(references[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := derived.ResolveEvidence(asset, unit)
	if err != nil || !strings.Contains(evidence.Content, target) {
		t.Fatalf("highest-ranked evidence did not select the final fact: evidence=%#v err=%v", evidence, err)
	}
}

func repeatEvidencePadding(pattern string, count int) string {
	if count <= 0 {
		return ""
	}
	runes := []rune(pattern)
	result := make([]rune, count)
	for index := range result {
		result[index] = runes[index%len(runes)]
	}
	return string(result)
}

func TestFocusedPassageIsNotBuriedByWholeSourceOrganization(t *testing.T) {
	content := "The relay depot lists inspection guidance.\n\n" + strings.Repeat("Ordinary logistical background. ", 300) +
		"\n\nThe relay depot's inspection fee is 37 credits. Keep the loading bay quiet.\n\n" +
		strings.Repeat("Ordinary logistical background. ", 300)
	asset := domain.Information{ID: "scope-density", Revision: 1, Content: content}
	whole := derived.EvidenceUnit{Schema: derived.EvidenceUnitSchema, SourceID: asset.ID,
		SourceRevision: 1, StartRune: 0, EndRune: len([]rune(content)), StartByte: 0, EndByte: len(content), Content: content}
	refs := selectEvidence(asset, "relay depot inspection fee", 3,
		[]evidenceChoice{{units: []derived.EvidenceUnit{whole}, terms: content}})
	if len(refs) == 0 || refs[0].ContentRunes > 3*derived.DefaultEvidenceUnitRunes {
		t.Fatalf("whole-source scope buried a focused passage: %v", refs)
	}
	unit, err := derived.ParseEvidenceUnitID(refs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := derived.ResolveEvidence(asset, unit)
	if err != nil || !strings.Contains(got.Content, "37 credits") || !strings.Contains(got.Content, "Keep the loading bay quiet") {
		t.Fatalf("fact or adjacent qualification lost: %v %v", got, err)
	}
}

func TestQueryEvidenceSummaryKeepsSourceAndBudget(t *testing.T) {
	first := "The north depot delivery costs 37 credits. Its loading bay must remain quiet."
	second := "The west depot delivery costs 52 credits. This charge already includes packaging."
	content := strings.Repeat("General background. ", 60) + "\n\n" + first + "\n\n" + strings.Repeat("Background. ", 60) + "\n\n" + second
	asset := domain.Information{ID: "preview-source", Revision: 1, Content: content}
	refs := rankEvidence(asset, "depot delivery credits", 3)
	old := strings.Repeat("Old representative metadata. ", 12)
	got := queryEvidenceSummary(asset, "depot delivery credits", old, refs)
	if len([]rune(got)) > len([]rune(old)) || len([]rune(got)) > derived.DefaultEvidenceUnitRunes {
		t.Fatalf("preview exceeded the existing summary allocation: %d", len([]rune(got)))
	}
	for _, line := range strings.Split(got, "\n[") {
		at := strings.Index(line, "] ")
		if at < 0 || !strings.Contains(content, line[at+2:]) {
			t.Fatalf("preview is not an original contiguous excerpt: %q", line)
		}
	}
	if strings.Contains(got, "Old representative metadata") || !strings.Contains(got, "credits") {
		t.Fatal(got)
	}
	refs[0].SourceID = "foreign"
	if queryEvidenceSummary(asset, "depot", old, refs) != old {
		t.Fatal("foreign reference accepted")
	}
}

func TestSentenceClueKeepsStatementWithinOneOriginalReference(t *testing.T) {
	statement := "Depot returns are permitted only before noon, after checking the original receipt, and only if the packaging remains unopened."
	text := "General depot return information is collected here. " + statement + " Unrelated reference details follow here."
	asset := domain.Information{ID: "sentence-source", Revision: 2, Content: text}
	refs := []domain.EvidenceReference{{SourceID: asset.ID, SourceRevision: 2, StartRune: 0, EndRune: len([]rune(text))}}
	got := queryEvidenceSummary(asset, "depot returns packaging receipt", strings.Repeat("x", 200), refs)
	if !strings.Contains(got, statement) {
		t.Fatalf("qualifications clipped: %q", got)
	}
	if utf8.RuneCountInString(got) > 200 {
		t.Fatal("preview budget expanded")
	}
}
