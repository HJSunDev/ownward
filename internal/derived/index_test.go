package derived

import (
	"math"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestIndexSearchesVectorsAndNavigatesBothDirections(t *testing.T) {
	index := NewIndex([]Record{
		{AssetID: "parent", AssetRevision: 1, Status: "ready", Embedding: []float32{1, 0}, Analysis: semantics.Analysis{Contexts: []semantics.InferredContext{{Key: "topic", Value: "react", Confidence: 0.9, Evidence: "explicit in test"}}}},
		{AssetID: "child", AssetRevision: 1, Status: "ready", Embedding: []float32{0.8, 0.2}, Analysis: semantics.Analysis{Relations: []semantics.Relation{{Type: "part_of", TargetID: "parent", Confidence: 0.95}}}},
		{AssetID: "other", AssetRevision: 1, Status: "ready", Embedding: []float32{0.9, 0.1}, Analysis: semantics.Analysis{Contexts: []semantics.InferredContext{{Key: "topic", Value: "vue", Confidence: 0.9, Evidence: "explicit in test"}}}},
	})
	hits := index.Search([]float32{1, 0}, []domain.Context{{Key: "topic", Value: "react"}}, 10)
	if len(hits) != 2 || hits[0].AssetID != "parent" || hits[1].AssetID != "child" {
		t.Fatalf("unexpected semantic hits: %#v", hits)
	}
	forward := index.Navigate([]string{"child"}, []string{"part_of"}, 3, 10)
	reverse := index.Navigate([]string{"parent"}, []string{"part_of"}, 3, 10)
	if len(forward) != 1 || len(reverse) != 1 || forward[0].TargetID != "parent" || reverse[0].SourceID != "child" {
		t.Fatalf("unexpected navigation: forward=%#v reverse=%#v", forward, reverse)
	}
}

func TestIndexDropsStaleInferredRelationsButKeepsExplicitRelations(t *testing.T) {
	index := NewIndex([]Record{
		{AssetID: "target", AssetRevision: 1, Status: "ready", Embedding: []float32{1, 0}},
		{AssetID: "inferred", AssetRevision: 1, Status: "ready", Embedding: []float32{0, 1}, Analysis: semantics.Analysis{Relations: []semantics.Relation{{Type: "related_to", TargetID: "target", TargetRevision: 1, Confidence: 0.9}}}},
		{AssetID: "explicit", AssetRevision: 1, Status: "ready", Embedding: []float32{0, 1}, Analysis: semantics.Analysis{Relations: []semantics.Relation{{Type: "supports", TargetID: "target", Confidence: 1}}}},
	})

	index.Upsert(Record{AssetID: "target", AssetRevision: 2, Status: "ready", Embedding: []float32{1, 0}})
	edges := index.Navigate([]string{"target"}, nil, 1, 10)
	if len(edges) != 1 || edges[0].SourceID != "explicit" || edges[0].Type != "supports" {
		t.Fatalf("unexpected relations after target update: %#v", edges)
	}
}

func TestIndexReportsOnlyInferredDependents(t *testing.T) {
	index := NewIndex([]Record{
		{AssetID: "target", AssetRevision: 1, Status: "ready", Embedding: []float32{1, 0}},
		{AssetID: "inferred", AssetRevision: 1, Status: "ready", Embedding: []float32{0, 1}, Analysis: semantics.Analysis{Relations: []semantics.Relation{{Type: "related_to", TargetID: "target", TargetRevision: 1, Confidence: 0.9}}}},
		{AssetID: "explicit", AssetRevision: 1, Status: "ready", Embedding: []float32{0, 1}, Analysis: semantics.Analysis{Relations: []semantics.Relation{{Type: "supports", TargetID: "target", Confidence: 1}}}},
	})
	dependents := index.Dependents("target")
	if len(dependents) != 1 || dependents[0] != "inferred" {
		t.Fatalf("unexpected inferred dependents: %#v", dependents)
	}
}

func TestIndexTracksOnlyUnresolvedSemanticWorkAsPendingDependencies(t *testing.T) {
	now := time.Unix(1, 0).UTC()
	work := semantics.Work{
		Schema:     semantics.WorkSchema,
		ID:         "work-source",
		Generation: "generation",
		Asset:      domain.Information{Schema: domain.AssetSchema, ID: "source", Revision: 1, Kind: domain.KindGeneral, Content: "source", CreatedAt: now, UpdatedAt: now},
		Candidates: []semantics.Candidate{{ID: "target", Revision: 1, Content: "target"}},
		CreatedAt:  now,
	}
	pending := Record{AssetID: "source", AssetRevision: 1, Status: "pending", SemanticWork: &work}
	index := NewIndex([]Record{pending})
	if actual := index.PendingDependents("target"); len(actual) != 1 || actual[0] != "source" {
		t.Fatalf("pending dependency was not indexed: %#v", actual)
	}

	accepted := semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: work.Asset.ID, Revision: work.Asset.Revision,
		Capability: semantics.Capability{ID: "test", Version: "1"}, Status: semantics.SubmissionComplete, AcceptedAt: now,
	}
	pending.SemanticResult = &accepted
	pending.Status = "ready"
	index.Upsert(pending)
	if actual := index.PendingDependents("target"); len(actual) != 0 {
		t.Fatalf("resolved semantic work remained pending: %#v", actual)
	}
}

func TestIndexRejectsNonFiniteVectors(t *testing.T) {
	index := NewIndex([]Record{{AssetID: "invalid", AssetRevision: 1, Status: "ready", Embedding: []float32{1, float32(math.NaN())}}})
	if index.HasVectors() {
		t.Fatal("non-finite vector was reported as searchable")
	}
	if hits := index.Search([]float32{1, 0}, nil, 10); len(hits) != 0 {
		t.Fatalf("unexpected hits: %#v", hits)
	}
	if hits := index.Search([]float32{1, float32(math.Inf(1))}, nil, 10); len(hits) != 0 {
		t.Fatalf("unexpected hits for invalid query: %#v", hits)
	}
}

func TestIndexTracksWhetherComparableVectorsExist(t *testing.T) {
	index := NewIndex(nil)
	if index.HasVectors() {
		t.Fatal("empty index reported a comparable vector")
	}
	index.Upsert(Record{AssetID: "one", AssetRevision: 1, Status: "ready", Embedding: []float32{1, 0}})
	if !index.HasVectors() {
		t.Fatal("valid vector was not reported")
	}
	index.Upsert(Record{AssetID: "one", AssetRevision: 2, Status: "pending"})
	if index.HasVectors() {
		t.Fatal("removed vector remained searchable")
	}
	index.Upsert(Record{AssetID: "one", AssetRevision: 3, Status: "ready", Embedding: []float32{0, 1}})
	if !index.HasVectors() {
		t.Fatal("restored vector was not reported")
	}
}

func TestIndexNormalizesLargeFiniteVectorsWithoutOverflow(t *testing.T) {
	large := float32(math.MaxFloat32)
	index := NewIndex([]Record{{AssetID: "large", AssetRevision: 1, Status: "ready", Embedding: []float32{large, large}}})
	hits := index.Search([]float32{large, large}, nil, 1)
	if len(hits) != 1 || hits[0].AssetID != "large" || math.IsNaN(hits[0].Score) || math.IsInf(hits[0].Score, 0) {
		t.Fatalf("unexpected large-vector result: %#v", hits)
	}
}

func TestIndexIgnoresAnOlderDerivedRevision(t *testing.T) {
	index := NewIndex([]Record{{AssetID: "one", AssetRevision: 2, Status: "ready", Embedding: []float32{1, 0}}})
	index.Upsert(Record{AssetID: "one", AssetRevision: 1, Status: "ready", Embedding: []float32{0, 1}})
	record, _ := index.Get("one")
	if record.AssetRevision != 2 {
		t.Fatalf("stale upsert replaced revision 2: %#v", record)
	}
	hits := index.Search([]float32{1, 0}, nil, 1)
	if len(hits) != 1 || hits[0].AssetID != "one" {
		t.Fatalf("stale vector replaced current vector: %#v", hits)
	}
}

func TestIndexSearchCacheReturnsIndependentResultsAndInvalidatesOnUpsert(t *testing.T) {
	index := NewIndex([]Record{{AssetID: "one", AssetRevision: 1, Status: "ready", Embedding: []float32{1, 0}}})
	first := index.Search([]float32{1, 0}, nil, 10)
	first[0].AssetID = "changed"

	cached := index.Search([]float32{1, 0}, nil, 10)
	if len(cached) != 1 || cached[0].AssetID != "one" {
		t.Fatalf("cached search result was mutable: %#v", cached)
	}

	index.Upsert(Record{AssetID: "one", AssetRevision: 2, Status: "ready", Embedding: []float32{0, 1}})
	if hits := index.Search([]float32{1, 0}, nil, 10); len(hits) != 0 {
		t.Fatalf("search cache survived an index update: %#v", hits)
	}
}
