package core

import (
	"strings"
	"testing"

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
