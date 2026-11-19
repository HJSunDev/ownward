package retrieval

import (
	"crypto/sha256"
	"sort"
	"strings"
	"testing"
)

func TestQueryTextScorePrefersSpecificFactOverRepeatedBoilerplate(t *testing.T) {
	query := "星砂档案的霜叶校验码是什么"
	fact := "星砂档案的中位记录指出霜叶校验码是七三一。"
	boilerplate := strings.Repeat("星砂档案常规背景。", 30)
	if QueryTextScore(query, fact) <= QueryTextScore(query, boilerplate) {
		t.Fatal("specific query evidence did not outrank repeated boilerplate")
	}
}

func TestStreamAndMemoryQueryTermsAndPassageScoringMatch(t *testing.T) {
	query := "Fiber 在 React 中解决什么问题 group42"
	var streamed []TermPosition
	if err := VisitQueryTerms(strings.NewReader(query), func(p TermPosition) error {
		streamed = append(streamed, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Slice(streamed, func(i, j int) bool {
		if streamed[i].Group != streamed[j].Group {
			return streamed[i].Group < streamed[j].Group
		}
		if streamed[i].Plane != streamed[j].Plane {
			return streamed[i].Plane < streamed[j].Plane
		}
		return streamed[i].Position < streamed[j].Position
	})
	terms := QueryTerms(query)
	if len(streamed) != len(terms) {
		t.Fatalf("term count mismatch: streamed=%d memory=%d", len(streamed), len(terms))
	}
	for i, term := range terms {
		if streamed[i].Digest != sha256.Sum256([]byte(term)) {
			t.Fatalf("term %d digest mismatch for %q", i, term)
		}
	}
	passages := []string{"React 组件背景说明", "Fiber 协调器工作单元可拆分、暂停与恢复"}
	memScorer := NewPassageTextScorer(query, passages)
	streamScorer, err := NewStreamPassageTextScorer(query, func(visit func(string) error) error {
		for _, p := range passages {
			if e := visit(p); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := streamScorer.ScoreReader(strings.NewReader(passages[1]))
	if err != nil {
		t.Fatal(err)
	}
	want := memScorer.Score(passages[1])
	if got != want {
		t.Fatalf("passage score mismatch: got %v want %v", got, want)
	}
}
