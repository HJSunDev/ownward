package boundedstore

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestRebuildPreservesRelationsAndRejectsChangedSources(t *testing.T) {
	s := retrievalStore(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		putRetrievalAsset(t, s, domain.Information{ID: id, Revision: 1, Content: "source " + id})
	}
	if e := s.CreateGeneration(ctx, "g", "space"); e != nil {
		t.Fatal(e)
	}
	records := []derived.Record{
		{AssetID: "a", AssetRevision: 1, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: "first", Relations: []semantics.Relation{{Type: "supports", TargetID: "b", TargetRevision: 1, Confidence: .9, Evidence: "original"}}}},
		{AssetID: "b", AssetRevision: 1, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: "second", Relations: []semantics.Relation{{Type: "supports", TargetID: "a", TargetRevision: 1, Confidence: .9, Evidence: "original"}}}},
	}
	for _, r := range records {
		putRecord(t, s, r)
	}
	if e := s.ActivateGeneration(ctx, "g", ""); e != nil {
		t.Fatal(e)
	}
	want, e := s.NavigatePage(ctx, "g", []string{"a"}, nil, 4, 10)
	if e != nil {
		t.Fatal(e)
	}
	build := func() (string, RetrievalStamp) {
		before, e := s.RetrievalStamp(ctx)
		if e != nil {
			t.Fatal(e)
		}
		id, e := s.BeginRebuild(ctx, "space")
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range records {
			data, _ := json.Marshal(r)
			v, e := s.StageOrganization(ctx, id, StringSource(data))
			if e != nil {
				t.Fatal(e)
			}
			if e = s.InstallRebuildOrganization(ctx, v); e != nil {
				t.Fatal(e)
			}
		}
		return id, before
	}
	id, before := build()
	if e = s.FinishRebuild(ctx, id, before); e != nil {
		t.Fatal(e)
	}
	got, e := s.NavigatePage(ctx, id, []string{"a"}, nil, 4, 10)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(want.Edges, got.Edges) || want.Examined != got.Examined {
		t.Fatal("relationship navigation changed")
	}
	if e = s.DrainMaintenance(ctx); e != nil {
		t.Fatal(e)
	}
	failed, before := build()
	putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: 2, Content: "new source a"})
	if e = s.FinishRebuild(ctx, failed, before); e == nil {
		t.Fatal("stale rebuild published")
	}
	active, _, e := s.Generation(ctx)
	if e != nil || active != id {
		t.Fatal("failed rebuild replaced active version", e)
	}
	if e = s.AbandonRebuild(ctx, failed); e != nil {
		t.Fatal(e)
	}
	if e = s.DrainMaintenance(ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = s.writer.QueryRowContext(ctx, "SELECT count(*) FROM generations WHERE id=?", failed).Scan(&count); e != nil || count != 0 {
		t.Fatal("abandoned candidate remains", count, e)
	}
}
