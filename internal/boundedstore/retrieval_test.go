package boundedstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func retrievalStore(t *testing.T) *Store {
	t.Helper()
	b, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
	s, e := Open(context.Background(), filepath.Join(t.TempDir(), "store.sqlite"), Options{Budget: b})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func putRetrievalAsset(t *testing.T, s *Store, a domain.Information) {
	t.Helper()
	ctx := context.Background()
	op := operation(fmt.Sprintf("%s/%d", a.ID, a.Revision))
	details, _ := json.Marshal(a)
	p, e := s.Stage(ctx, operationKey(op), StringSource(a.Content), StringSource(details))
	if e != nil {
		t.Fatal(e)
	}
	v := AssetWrite{Meta: contract.AssetMeta{ID: a.ID, Revision: a.Revision, Kind: domain.KindGeneral, CreatedAt: time.Unix(100, 0).UTC(), UpdatedAt: time.Unix(100+int64(a.Revision), 0).UTC()}, ExpectedRevision: a.Revision - 1, Payload: p}
	if e = s.Publish(ctx, receipt(op, v), []AssetWrite{v}); e != nil {
		t.Fatal(e)
	}
}

func putRecord(t *testing.T, s *Store, r derived.Record) OrganizationVersion {
	t.Helper()
	data, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.StageOrganization(context.Background(), "g", StringSource(data))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.PublishOrganization(context.Background(), v); e != nil {
		t.Fatal(e)
	}
	return v
}

func bruteForceVectorSearch(records []derived.Record, query []float32, filters []domain.Context, limit int) []Ranked {
	var qNorm float64
	for _, v := range query {
		qNorm += float64(v) * float64(v)
	}
	qNorm = math.Sqrt(qNorm)
	var hits []Ranked
	for _, r := range records {
		if len(r.Embedding) != len(query) || qNorm == 0 {
			continue
		}
		if !testContextMatches(semantics.ContextValues(r.Analysis.Contexts), filters) {
			continue
		}
		var dot, dNorm float64
		for i := range query {
			dot += float64(query[i]) * float64(r.Embedding[i])
			dNorm += float64(r.Embedding[i]) * float64(r.Embedding[i])
		}
		dNorm = math.Sqrt(dNorm)
		if dNorm == 0 {
			continue
		}
		score := dot / (qNorm * dNorm)
		if score <= 0 {
			continue
		}
		hits = append(hits, Ranked{ID: r.AssetID, Score: score})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].ID < hits[j].ID
		}
		return hits[i].Score > hits[j].Score
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func testContextMatches(actual, required []domain.Context) bool {
	for _, expected := range required {
		declared, compatible := false, false
		for _, candidate := range actual {
			if strings.EqualFold(candidate.Key, expected.Key) {
				declared = true
				if strings.EqualFold(candidate.Value, expected.Value) {
					compatible = true
				}
			}
		}
		if declared && !compatible {
			return false
		}
	}
	return true
}

func TestPersistentLexicalSearchAndContextFiltering(t *testing.T) {
	s := retrievalStore(t)
	ctx := context.Background()
	values := []domain.Information{
		{ID: "a", Revision: 1, Content: "中文关系检索 alpha café Ω Σ 你好你好", Contexts: []domain.Context{{Key: "Σ", Value: "Work"}}},
		{ID: "b", Revision: 1, Content: "中文 原文更新 Straße alpha alpha beta", Relations: []domain.ExplicitRelation{{Type: "related_to", TargetID: "a"}}},
		{ID: "c", Revision: 1, Content: "你好，组织关系；日本語 한국어🙂", Contexts: []domain.Context{{Key: "ς", Value: "Other"}}},
	}
	for _, a := range values {
		putRetrievalAsset(t, s, a)
	}
	gotID, e := s.LexicalSearch(ctx, "a", nil, 10)
	if e != nil || len(gotID) == 0 || gotID[0].ID != "a" {
		t.Fatalf("expected asset 'a' ranked first, got %+v (%v)", gotID, e)
	}
	gotFiltered, e := s.LexicalSearch(ctx, "你好", []domain.Context{{Key: "σ", Value: "work"}}, 10)
	if e != nil {
		t.Fatal(e)
	}
	for _, hit := range gotFiltered {
		if hit.ID == "c" {
			t.Fatalf("conflicting context asset 'c' was not excluded: %+v", gotFiltered)
		}
	}
	gotMissing, e := s.LexicalSearch(ctx, "missing", nil, 10)
	if e != nil || len(gotMissing) != 0 {
		t.Fatalf("expected no hits for missing term, got %+v (%v)", gotMissing, e)
	}

	values[0].Revision++
	values[0].Content = "replacement unrelated"
	putRetrievalAsset(t, s, values[0])
	gotAfterUpdate, e := s.LexicalSearch(ctx, "中文 alpha", nil, 10)
	if e != nil {
		t.Fatal(e)
	}
	if len(gotAfterUpdate) == 0 || gotAfterUpdate[0].ID != "b" {
		t.Fatalf("expected updated 'a' to drop below 'b', got %+v", gotAfterUpdate)
	}
	for _, hit := range gotAfterUpdate {
		if hit.ID == "a" {
			t.Fatalf("replaced terms from 'a' remained searchable: %+v", gotAfterUpdate)
		}
	}
}

func TestPersistentVectorsEquivalentAcrossPackingAndCorruption(t *testing.T) {
	s := retrievalStore(t)
	ctx := context.Background()
	if e := s.CreateGeneration(ctx, "g", "space"); e != nil {
		t.Fatal(e)
	}
	rng := rand.New(rand.NewPCG(17, 41))
	records := make([]derived.Record, 270)
	for i := range records {
		id := fmt.Sprintf("a%04d", i)
		putRetrievalAsset(t, s, domain.Information{ID: id, Revision: 1, Content: "raw"})
		v := make([]float32, 512)
		for j := range v {
			v[j] = float32(rng.NormFloat64())
		}
		if i == 1 {
			copy(v, records[0].Embedding)
		}
		records[i] = derived.Record{AssetID: id, AssetRevision: 1, Status: "ready", EmbeddingSpace: "space", Embedding: v, InputsKnown: true, Analysis: semantics.Analysis{Summary: "raw"}}
		if i%3 == 0 {
			records[i].Analysis.Contexts = []semantics.InferredContext{{Key: "scope", Value: "work"}}
		}
		if i%3 == 1 {
			records[i].Analysis.Contexts = []semantics.InferredContext{{Key: "scope", Value: "home"}}
		}
		putRecord(t, s, records[i])
	}
	vectors := make([][]float32, len(records))
	for i, r := range records {
		vectors[i] = append([]float32(nil), r.Embedding...)
	}
	var baseline map[string][]Ranked
	check := func() {
		t.Helper()
		for _, qi := range []int{0, 1, 88, 260} {
			for fi, filters := range [][]domain.Context{nil, {{Key: "scope", Value: "work"}}} {
				got, _, e := s.VectorSearch(ctx, "g", "space", vectors[qi], filters, 10)
				if e != nil {
					t.Fatal(e)
				}
				want := bruteForceVectorSearch(records, vectors[qi], filters, 10)
				if len(got) != len(want) {
					t.Fatalf("count %v vs %v", got, want)
				}
				for i := range got {
					if got[i].ID != want[i].ID || math.Abs(got[i].Score-want[i].Score) > 1e-6 {
						t.Fatalf("q%d rank%d %+v vs %+v", qi, i, got[i], want[i])
					}
				}
				key := fmt.Sprintf("%d:%d", qi, fi)
				if baseline == nil {
					baseline = map[string][]Ranked{}
				}
				if prev, exists := baseline[key]; exists && !reflect.DeepEqual(got, prev) {
					t.Fatalf("packing/corruption drifted vector results for %s: %+v vs %+v", key, got, prev)
				}
				baseline[key] = got
			}
		}
	}
	check()
	if _, e := s.PackVectors(ctx, "space", true); e != nil {
		t.Fatal(e)
	}
	check()
	if e := s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "UPDATE vector_filter_pages SET digest=zeroblob(32) WHERE level=1")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	check()
	putRetrievalAsset(t, s, domain.Information{ID: "a0000", Revision: 2, Content: "changed"})
	got, _, e := s.VectorSearch(ctx, "g", "space", vectors[0], nil, 400)
	if e != nil {
		t.Fatal(e)
	}
	for _, h := range got {
		if h.ID == "a0000" {
			t.Fatal("stale vector delivered")
		}
	}
}

func TestPersistentNavigationAndDependencyValidity(t *testing.T) {
	s := retrievalStore(t)
	ctx := context.Background()
	if e := s.CreateGeneration(ctx, "g", "space"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 9; i++ {
		id := fmt.Sprintf("n%d", i)
		putRetrievalAsset(t, s, domain.Information{ID: id, Revision: 1, Content: "source " + id})
		r := derived.Record{AssetID: id, AssetRevision: 1, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: "source"}}
		if i > 0 {
			r.Analysis.Relations = []semantics.Relation{{Type: "supports", TargetID: fmt.Sprintf("n%d", i-1), TargetRevision: 1, Confidence: .9, Evidence: "original"}}
		}
		putRecord(t, s, r)
	}
	if e := s.ActivateGeneration(ctx, "g", ""); e != nil {
		t.Fatal(e)
	}
	var collected []Edge
	var token string
	for n := 0; n < 8; n++ {
		start := []string{"n4"}
		if n > 0 {
			start = []string{token}
		}
		got, e := s.NavigatePage(ctx, "g", start, nil, 4, 2)
		if e != nil {
			t.Fatal(e)
		}
		collected = append(collected, got.Edges...)
		token = got.Continuation
		if !got.Incomplete {
			break
		}
	}
	if len(collected) != 8 {
		t.Fatalf("expected all 8 chain edges across pages, got %d: %+v", len(collected), collected)
	}
	page, e := s.NavigatePage(ctx, "g", []string{"n4"}, nil, 4, 1)
	if e != nil {
		t.Fatal(e)
	}
	putRetrievalAsset(t, s, domain.Information{ID: "n0", Revision: 2, Content: "changed"})
	if _, e = s.CurrentOrganization(ctx, "g", "n1"); e == nil {
		t.Fatal("dependent organization remained valid")
	}
	if _, e = s.NavigatePage(ctx, "g", []string{page.Continuation}, nil, 4, 1); e == nil {
		t.Fatal("stale navigation continuation accepted")
	}
}
