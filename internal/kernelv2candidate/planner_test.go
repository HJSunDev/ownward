package kernelv2candidate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/domain"
)

func TestEvidencePlansPreservesSingleSourceDepth(t *testing.T) {
	plans := NewEvidencePlans()
	plans.ObserveSearch("harbor status", []string{"deep", "short-a", "short-b"})
	got, planned := plans.References("harbor status", "deep", 3, testPlanReader, testPlanRanker)
	if !planned || len(got) != 3 {
		t.Fatalf("single deep source must preserve bounded depth: planned=%v got=%v", planned, got)
	}
	short, planned := plans.References("harbor status", "short-a", 3, testPlanReader, testPlanRanker)
	if !planned || short != nil {
		t.Fatalf("short lane must reuse the empty evidence probe and retain full-read fallback: planned=%v got=%v", planned, short)
	}
}

func TestEvidencePlansPrefersSourceBreadthBeforeRepeatedDepth(t *testing.T) {
	plans := NewEvidencePlans()
	plans.ObserveSearch("harbor status", []string{"deep", "deep-b", "deep-c", "short-a"})
	rankCalls := 0
	rank := func(value domain.Information, query string, limit int) []domain.EvidenceReference {
		rankCalls++
		return testPlanRanker(value, query, limit)
	}
	for _, sourceID := range []string{"deep", "deep-b", "deep-c"} {
		got, planned := plans.References("harbor status", sourceID, 3, testPlanReader, rank)
		if !planned || len(got) != 1 || got[0].SourceID != sourceID {
			t.Fatalf("competing deep source %q must expose one breadth lane first: planned=%v got=%v", sourceID, planned, got)
		}
	}
	first, _ := plans.References("harbor status", "deep", 3, testPlanReader, rank)
	second, _ := plans.References("harbor status", "deep", 3, testPlanReader, rank)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same query plan must be deterministic: first=%v second=%v", first, second)
	}
	if rankCalls != 3 {
		t.Fatalf("planner must stop probing after two deep lanes and lazily rank only requested remainder: %d", rankCalls)
	}
}

func TestEvidencePlansDoesNotInventAPlan(t *testing.T) {
	plans := NewEvidencePlans()
	if got, planned := plans.References("unknown", "deep", 3, testPlanReader, testPlanRanker); planned || got != nil {
		t.Fatalf("unobserved query must use the unchanged fallback: planned=%v got=%v", planned, got)
	}
}

func testPlanReader(sourceID string) (domain.Information, bool) {
	return domain.Information{ID: sourceID, Revision: 1, Content: sourceID}, true
}

func testPlanRanker(value domain.Information, _ string, limit int) []domain.EvidenceReference {
	if strings.HasPrefix(value.ID, "short-") {
		return nil
	}
	values := []domain.EvidenceReference{
		{SourceID: value.ID, SourceRevision: value.Revision, StartRune: 0, EndRune: 1},
		{SourceID: value.ID, SourceRevision: value.Revision, StartRune: 1, EndRune: 2},
		{SourceID: value.ID, SourceRevision: value.Revision, StartRune: 2, EndRune: 3},
	}
	if len(values) > limit {
		values = values[:limit]
	}
	return values
}
