package kernelv2candidate

import (
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HJSunDev/ownward/internal/domain"
)

func TestEvidencePlansPreservesSingleSourceDepth(t *testing.T) {
	plans := NewEvidencePlans()
	plans.ObserveSearch("harbor status", []string{"deep", "short-a", "short-b"})
	got, planned := plans.References("harbor status", "deep", 3, testPlanReader, testPlanRanker, testPlanProber)
	if !planned || len(got) != 3 {
		t.Fatalf("single deep source must preserve bounded depth: planned=%v got=%v", planned, got)
	}
	short, planned := plans.References("harbor status", "short-a", 3, testPlanReader, testPlanRanker, testPlanProber)
	if !planned || short != nil {
		t.Fatalf("short lane must reuse the empty evidence probe and retain full-read fallback: planned=%v got=%v", planned, short)
	}
}

func TestEvidencePlansPrefersSourceBreadthBeforeRepeatedDepth(t *testing.T) {
	plans := NewEvidencePlans()
	plans.ObserveSearch("harbor status", []string{"deep", "deep-b", "deep-c", "short-a"})
	var probeCalls atomic.Int32
	rank := func(value domain.Information, query string, limit int) []domain.EvidenceReference {
		return testPlanRanker(value, query, limit)
	}
	probe := func(value domain.Information, query string) ([]domain.EvidenceReference, bool) {
		probeCalls.Add(1)
		return testPlanProber(value, query)
	}
	for _, sourceID := range []string{"deep", "deep-b", "deep-c"} {
		got, planned := plans.References("harbor status", sourceID, 3, testPlanReader, rank, probe)
		if !planned || len(got) != 1 || got[0].SourceID != sourceID {
			t.Fatalf("competing deep source %q must expose one breadth lane first: planned=%v got=%v", sourceID, planned, got)
		}
	}
	first, _ := plans.References("harbor status", "deep", 3, testPlanReader, rank, probe)
	second, _ := plans.References("harbor status", "deep", 3, testPlanReader, rank, probe)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same query plan must be deterministic: first=%v second=%v", first, second)
	}
	if probeCalls.Load() != 4 {
		t.Fatalf("planner must probe the fixed returned-source bound once while preserving deterministic lanes: %d", probeCalls.Load())
	}
}

func TestEvidencePlansDoesNotInventAPlan(t *testing.T) {
	plans := NewEvidencePlans()
	if got, planned := plans.References("unknown", "deep", 3, testPlanReader, testPlanRanker, testPlanProber); planned || got != nil {
		t.Fatalf("unobserved query must use the unchanged fallback: planned=%v got=%v", planned, got)
	}
}

func TestEvidencePlansReusesAlreadyValidatedCurrentSourceDuringFirstPreparation(t *testing.T) {
	plans := NewEvidencePlans()
	plans.ObserveSearch("harbor status", []string{"deep", "deep-b", "deep-c"})
	current, _ := testPlanReader("deep")
	var currentReads atomic.Int32
	reader := func(sourceID string) (domain.Information, bool) {
		if sourceID == current.ID {
			currentReads.Add(1)
		}
		return testPlanReader(sourceID)
	}
	got, planned := plans.ReferencesWithCurrent("harbor status", current, 3, reader, testPlanRanker, testPlanProber)
	if !planned || len(got) != 1 {
		t.Fatalf("expected the bounded breadth plan: planned=%v got=%v", planned, got)
	}
	if currentReads.Load() != 0 {
		t.Fatalf("the public boundary's validated source must not be read again: %d", currentReads.Load())
	}
}

func TestEvidencePlansCachesOnlyItsBoundedCurrentEvidence(t *testing.T) {
	content := strings.Repeat("The harbor status confirms the cobalt beacon. ", 40)
	value := domain.Information{ID: "deep", Revision: 7, Content: content}
	reader := func(sourceID string) (domain.Information, bool) {
		if sourceID != value.ID {
			return domain.Information{}, false
		}
		return value, true
	}
	plans := NewEvidencePlans()
	plans.ObserveSearch("cobalt beacon", []string{value.ID})
	references, planned := plans.References("cobalt beacon", value.ID, 2, reader, RankEvidence, ProbeEvidence)
	if !planned || len(references) == 0 {
		t.Fatalf("expected a bounded evidence plan: planned=%v references=%v", planned, references)
	}
	cached, exists := plans.Read(references[0].ID, reader)
	if !exists || cached.ID != references[0].ID || !strings.Contains(cached.Content, "cobalt beacon") {
		t.Fatalf("planned evidence was not read through exactly: exists=%v cached=%+v", exists, cached)
	}
	if _, exists := plans.Read(references[0].ID+"tampered", reader); exists {
		t.Fatal("caller-supplied evidence identity must not enter the read-through path")
	}
	value.Revision++
	if _, exists := plans.Read(references[0].ID, reader); exists {
		t.Fatal("stale planned evidence must fall back to full authority validation")
	}
}

func TestEvidencePlansCachedPathsAreInvalidatedByAuthorityMutation(t *testing.T) {
	content := strings.Repeat("The harbor status confirms the cobalt beacon. ", 40)
	value := domain.Information{ID: "deep", Revision: 7, Content: content}
	plans := NewEvidencePlans()
	plans.ObserveSearch("cobalt beacon", []string{value.ID, "deep-b"})
	reader := func(sourceID string) (domain.Information, bool) {
		if sourceID == value.ID {
			return value, true
		}
		return domain.Information{ID: sourceID, Revision: 1, Content: content}, true
	}
	references, planned := plans.ReferencesWithCurrent("cobalt beacon", value, 2, reader, RankEvidence, ProbeEvidence)
	if !planned || len(references) != 1 {
		t.Fatalf("expected a prepared breadth lane: planned=%v references=%v", planned, references)
	}
	if cached, ok := plans.CachedReferences("cobalt beacon", value.ID, 2); !ok || !reflect.DeepEqual(cached, references) {
		t.Fatalf("prepared reference was not reused exactly: ok=%v cached=%v", ok, cached)
	}
	if cached, ok := plans.ReadCached(references[0].ID); !ok || cached.SourceRevision != value.Revision {
		t.Fatalf("prepared evidence was not reused exactly: ok=%v cached=%+v", ok, cached)
	}
	plans.Reset()
	if cached, ok := plans.CachedReferences("cobalt beacon", value.ID, 2); ok || cached != nil {
		t.Fatalf("authority mutation must invalidate prepared references: ok=%v cached=%v", ok, cached)
	}
	if _, ok := plans.ReadCached(references[0].ID); ok {
		t.Fatal("authority mutation must invalidate prepared evidence")
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

func testPlanProber(value domain.Information, query string) ([]domain.EvidenceReference, bool) {
	values := testPlanRanker(value, query, planningEvidenceProbe)
	if len(values) == 0 {
		return nil, false
	}
	return values[:1], len(values) > 1
}
