package boundedstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestPersistentNavigationOrderAfterUpdatingEarlierSource(t *testing.T) {
	s := retrievalStore(t)
	ctx := context.Background()
	if e := s.CreateGeneration(ctx, "g", "space"); e != nil {
		t.Fatal(e)
	}
	var records []derived.Record
	for _, id := range []string{"target", "a", "b"} {
		putRetrievalAsset(t, s, domain.Information{ID: id, Revision: 1, Content: "source " + id})
		r := derived.Record{AssetID: id, AssetRevision: 1, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: "source"}}
		if id != "target" {
			r.Analysis.Relations = []semantics.Relation{{Type: "supports", TargetID: "target", TargetRevision: 1, Confidence: 1, Evidence: "source evidence"}}
		}
		records = append(records, r)
		putRecord(t, s, r)
	}
	if e := s.ActivateGeneration(ctx, "g", ""); e != nil {
		t.Fatal(e)
	}
	records[1].AssetRevision = 2
	putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: 2, Content: "updated source a"})
	putRecord(t, s, records[1])

	got, e := s.NavigatePage(ctx, "g", []string{"target"}, nil, 1, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(got.Edges) != 1 || got.Edges[0].SourceID != "b" || !got.Incomplete {
		t.Fatalf("unexpected first-page navigation candidate after source update: %+v", got)
	}
	second, e := s.NavigatePage(ctx, "g", []string{got.Continuation}, nil, 1, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(second.Edges) != 1 || second.Edges[0].SourceID != "a" {
		t.Fatalf("unexpected second-page navigation candidate: %+v", second)
	}
	if second.Incomplete {
		third, e := s.NavigatePage(ctx, "g", []string{second.Continuation}, nil, 1, 1)
		if e != nil || len(third.Edges) != 0 || third.Incomplete {
			t.Fatalf("expected exhausted third page, got %+v (%v)", third, e)
		}
	}
}

func TestPersistentExplicitPriorityBothDirections(t *testing.T) {
	for _, direction := range []string{"outgoing", "incoming"} {
		for _, explicitOwner := range []string{"", "a", "b"} {
			t.Run(direction+"/explicit="+explicitOwner, func(t *testing.T) {
				s := retrievalStore(t)
				ctx := context.Background()
				if err := s.CreateGeneration(ctx, "g", "space"); err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"a", "b"} {
					a := domain.Information{ID: id, Revision: 1, Content: "source " + id}
					if id == explicitOwner {
						target := "b"
						if id == "b" {
							target = "a"
						}
						a.Relations = []domain.ExplicitRelation{{Type: "supports", TargetID: target}}
					}
					putRetrievalAsset(t, s, a)
				}
				for _, id := range []string{"a", "b"} {
					r := derived.Record{AssetID: id, AssetRevision: 1, InputsKnown: true, Status: "ready"}
					if id == "a" {
						r.Analysis.Relations = []semantics.Relation{{Type: "contradicts", Direction: direction, TargetID: "b", TargetRevision: 1, Confidence: 1}}
					}
					putRecord(t, s, r)
				}
				if err := s.ActivateGeneration(ctx, "g", ""); err != nil {
					t.Fatal(err)
				}
				page, err := s.NavigatePage(ctx, "g", []string{"a"}, nil, 1, 20)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, e := range page.Edges {
					found = found || e.Type == "contradicts"
				}
				want := explicitOwner != "a" && !(direction == "incoming" && explicitOwner == "b")
				if found != want {
					t.Fatalf("inferred edge=%v want=%v: %+v", found, want, page.Edges)
				}
			})
		}
	}
}

func TestPersistentGroundedPagesKeepPublicationOrderAcrossRestart(t *testing.T) {
	s := retrievalStore(t)
	ctx := context.Background()
	if err := s.CreateGeneration(ctx, "g", "space"); err != nil {
		t.Fatal(err)
	}
	var records []derived.Record
	for _, id := range []string{"a", "b", "target"} {
		putRetrievalAsset(t, s, domain.Information{ID: id, Revision: 1, Content: "source " + id})
		r := derived.Record{AssetID: id, AssetRevision: 1, Status: "ready", InputsKnown: true}
		if id != "target" {
			r.Analysis.Organization = &semantics.Organization{Schema: semantics.OrganizationSchema, Snapshot: "org_" + id, Links: []semantics.GroundedLink{{ID: "rel_" + id, Type: "supports", Meaning: "source support", Source: semantics.GraphEndpoint{AssetID: id, Revision: 1}, Target: semantics.GraphEndpoint{AssetID: "target", Revision: 1}}}}
		}
		records = append(records, r)
	}
	for _, r := range records {
		putRecord(t, s, r)
	}
	if err := s.ActivateGeneration(ctx, "g", ""); err != nil {
		t.Fatal(err)
	}
	r := records[0]
	r.AssetRevision = 2
	r.Analysis.Organization.Links[0].Source.Revision = 2
	putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: 2, Content: "updated source a"})
	putRecord(t, s, r)
	path, budget := filepath.Join(s.directory, "store.sqlite"), s.budget
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = Open(ctx, path, Options{Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	firstPage, err := s.NavigatePage(ctx, "g", []string{"target"}, nil, 1, 1)
	if err != nil || len(firstPage.Edges) != 1 || firstPage.Edges[0].Grounded == nil || firstPage.Edges[0].Grounded.ID != "rel_b" || !firstPage.Incomplete {
		t.Fatalf("unexpected first grounded page after restart: %+v (%v)", firstPage, err)
	}
	secondPage, err := s.NavigatePage(ctx, "g", []string{firstPage.Continuation}, nil, 1, 1)
	if err != nil || len(secondPage.Edges) != 1 || secondPage.Edges[0].Grounded == nil || secondPage.Edges[0].Grounded.ID != "rel_a" {
		t.Fatalf("unexpected second grounded page after restart: %+v (%v)", secondPage, err)
	}
	if secondPage.Incomplete {
		thirdPage, err := s.NavigatePage(ctx, "g", []string{secondPage.Continuation}, nil, 1, 1)
		if err != nil || len(thirdPage.Edges) != 0 || thirdPage.Incomplete {
			t.Fatalf("expected exhausted grounded third page, got %+v (%v)", thirdPage, err)
		}
	}

	var adjacent []string
	if err = s.VisitAdjacent(ctx, "g", "target", 0, func(e Edge, valid bool) error {
		if valid {
			adjacent = append(adjacent, e.OwnerID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(adjacent, []string{"b", "a"}) {
		t.Fatal(adjacent)
	}
}

func TestPersistentMixedDeliveryChecksMakeProgress(t *testing.T) {
	s := retrievalStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stamp, e := s.RetrievalStamp(ctx)
	if e != nil {
		t.Fatal(e)
	}
	held, release := make(chan struct{}), make(chan struct{})
	writerDone := make(chan error, 1)
	go func() { writerDone <- s.write(ctx, func(tx *sql.Tx) error { close(held); <-release; return nil }) }()
	<-held
	savedDone := make(chan error, 1)
	go func() { savedDone <- s.AuthorizeDelivery(ctx, nil) }()
	time.Sleep(15 * time.Millisecond)
	retrievedDone := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { retrievedDone <- s.AuthorizeRetrieval(ctx, stamp) }()
	}
	deadline := time.Now().Add(150 * time.Millisecond)
	for len(s.readers) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.readers) != 0 {
		close(release)
		t.Fatal("failed to arrange overlapping delivery checks")
	}
	close(release)
	if e := <-writerDone; e != nil {
		t.Fatal(e)
	}
	savedErr := <-savedDone
	one, two := <-retrievedDone, <-retrievedDone
	if savedErr != nil || one != nil || two != nil {
		t.Error("completed write leaves reader/mutex lock cycle until request deadline")
	}
}
