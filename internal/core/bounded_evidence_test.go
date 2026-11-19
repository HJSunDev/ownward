package core

import (
	"context"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/retrieval"
)

func TestBoundedEvidencePreservesExistingSelection(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("无关内容中文🙂 alpha ", 130) + "\n\n目标事实 beta evidence。\n\n" + strings.Repeat("tail text ", 170),
		strings.Repeat("word ", 300) + "\r\n\r\n" + strings.Repeat("beta 目标关系。", 250),
		strings.Repeat("长", 1700) + "目标。",
	} {
		ctx := context.Background()
		b, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
		s, e := boundedstore.Open(ctx, filepath.Join(t.TempDir(), "test.sqlite"), boundedstore.Options{Budget: b})
		if e != nil {
			t.Fatal(e)
		}
		op := contract.OperationIdentity{ID: "evidence", System: "s", Principal: "p", Generation: 1, Kind: "ownward_create", Digest: "evidence"}
		key, e := boundedstore.OperationKey(op)
		if e != nil {
			t.Fatal(e)
		}
		p, e := s.Stage(ctx, key, boundedstore.StringSource(text), nil)
		if e != nil {
			t.Fatal(e)
		}
		v := boundedstore.AssetWrite{Meta: contract.AssetMeta{ID: "a", Revision: 1, Kind: domain.KindGeneral, CreatedAt: time.Now(), UpdatedAt: time.Now()}, Payload: p}
		e = s.Publish(ctx, contract.MutationReceipt{Operation: op, Results: []contract.MutationOutcome{{Asset: contract.AssetVersion{ID: "a", Revision: 1}}}}, []boundedstore.AssetWrite{v})
		if e != nil {
			t.Fatal(e)
		}
		for _, query := range []string{"目标 beta", "alpha word", "不存在", "长"} {
			for _, limit := range []int{1, 3, 8} {
				got, e := s.RankEvidence(ctx, "", "a", query, limit)
				if e != nil {
					t.Fatal(e)
				}
				want := rankEvidence(domain.Information{ID: "a", Revision: 1, Content: text}, query, limit)
				if len(got) != len(want) || len(got) > 0 && !reflect.DeepEqual(got, want) {
					t.Fatalf("query%q limit%d\ngot %+v\nwant %+v", query, limit, got, want)
				}
			}
		}
		s.Close()
	}
}

type evidenceChoice struct {
	units []derived.EvidenceUnit
	terms string
	score float64
}

func rankEvidence(value domain.Information, query string, limit int) []domain.EvidenceReference {
	return selectEvidence(value, query, limit, nil)
}

func selectEvidence(value domain.Information, query string, limit int, choices []evidenceChoice) []domain.EvidenceReference {
	units := derived.EvidenceRanges(value)
	if limit <= 0 {
		return nil
	}
	passages := make([]string, 0, len(units))
	for _, unit := range units {
		passages = append(passages, unit.Content)
	}
	scorer := retrieval.NewPassageTextScorer(query, passages)
	for _, unit := range units {
		choices = append(choices, evidenceChoice{units: []derived.EvidenceUnit{completePassage(value, unit)}, terms: unit.Content})
	}
	for i := range choices {
		width := 0
		for _, unit := range choices[i].units {
			width += unit.EndRune - unit.StartRune
		}
		normalizer := math.Sqrt(math.Max(1, float64(width)/float64(derived.DefaultEvidenceUnitRunes)))
		choices[i].score = scorer.Score(choices[i].terms) / normalizer
	}
	sort.SliceStable(choices, func(left, right int) bool {
		if choices[left].score == choices[right].score {
			return choices[left].units[0].StartRune < choices[right].units[0].StartRune
		}
		return choices[left].score > choices[right].score
	})
	result := make([]domain.EvidenceReference, 0, limit)
	for _, choice := range choices {
		if choice.score <= 0 {
			break
		}
		var pending []domain.EvidenceReference
		for _, candidate := range choice.units {
			covered := false
			for _, ref := range result {
				covered = covered || (ref.StartRune <= candidate.StartRune && ref.EndRune >= candidate.EndRune)
			}
			if covered {
				continue
			}
			unit, err := derived.MaterializeEvidenceUnit(value, candidate)
			if err != nil {
				pending = nil
				break
			}
			pending = append(pending, unit.Reference())
		}
		if len(result)+len(pending) > limit {
			continue
		}
		result = append(result, pending...)
		if len(result) == limit {
			break
		}
	}
	return result
}

func completePassage(value domain.Information, unit derived.EvidenceUnit) derived.EvidenceUnit {
	for n := 0; n < derived.DefaultEvidenceUnitRunes && unit.StartByte > 0; n++ {
		if strings.HasSuffix(value.Content[:unit.StartByte], "\n\n") || strings.HasSuffix(value.Content[:unit.StartByte], "\r\n\r\n") {
			break
		}
		_, size := utf8.DecodeLastRuneInString(value.Content[:unit.StartByte])
		unit.StartByte -= size
		unit.StartRune--
	}
	for n := 0; n < derived.DefaultEvidenceUnitRunes && unit.EndByte < len(value.Content); n++ {
		if strings.HasSuffix(value.Content[:unit.EndByte], "\n\n") || strings.HasSuffix(value.Content[:unit.EndByte], "\r\n\r\n") {
			break
		}
		_, size := utf8.DecodeRuneInString(value.Content[unit.EndByte:])
		unit.EndByte += size
		unit.EndRune++
	}
	unit.Content = value.Content[unit.StartByte:unit.EndByte]
	return unit
}
