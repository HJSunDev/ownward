package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestOwnerRelationsExpiredContinuationRequiresRefresh(t *testing.T) {
	f := ownerHTTP(t)
	var ids []string
	for _, body := range []string{"first fact", "second fact"} {
		result, err := f.k.Create(f.ctx, contract.CreateInput{Content: body})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, result.Information.ID)
	}
	const generation = "review-relations"
	if err := f.s.CreateGeneration(f.ctx, generation, "fixture-space"); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		record := derived.Record{AssetID: id, AssetRevision: 1, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: "fixture"}}
		if i == 0 {
			record.Analysis.Organization = &semantics.Organization{Schema: semantics.OrganizationSchema, Snapshot: "review", Links: []semantics.GroundedLink{{ID: "related", Type: "supports", Meaning: "first supports second", Source: semantics.GraphEndpoint{AssetID: ids[0], Revision: 1}, Target: semantics.GraphEndpoint{AssetID: ids[1], Revision: 1}}}}
		}
		b, _ := json.Marshal(record)
		staged, err := f.s.StageOrganization(f.ctx, generation, boundedstore.StringSource(b))
		if err != nil {
			t.Fatal(err)
		}
		if err = f.s.PublishOrganization(f.ctx, staged); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.ActivateGeneration(f.ctx, generation, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DrainMaintenance(f.ctx); err != nil {
		t.Fatal(err)
	}
	assets := f.query(t, contract.OwnerQuery{View: "assets"})
	query := contract.OwnerQuery{View: "relations", Handle: assets.Assets[0].Handle, Limit: 1}
	freshPage := func() contract.OwnerPage {
		t.Helper()
		// Fresh read-only projections may be interrupted by maintenance. Follow
		// the public refresh contract; never retry a failed mutation implicitly.
		for attempt := 0; attempt < 4; attempt++ {
			response, body := f.request(t, "query", query, nil)
			if response.StatusCode == http.StatusConflict {
				t.Log("fresh projection requested another read")
				continue
			}
			if response.StatusCode != http.StatusOK {
				t.Fatalf("fresh projection: %d %s", response.StatusCode, body)
			}
			var page contract.OwnerPage
			if err := json.Unmarshal(body, &page); err != nil {
				t.Fatal(err)
			}
			return page
		}
		t.Fatal("fresh projection did not recover within four reads")
		return contract.OwnerPage{}
	}
	page := f.query(t, query)
	if len(page.Relations) != 1 || page.Next == "" {
		t.Fatalf("fixture has no continuation: %+v", page)
	}
	query.After = page.Next
	before, _ := f.request(t, "query", query, nil)
	if before.StatusCode != http.StatusOK {
		t.Fatal("live continuation failed", before.StatusCode)
	}
	paths, err := filepath.Glob(filepath.Join(f.root, "stores", "*", "ownward.sqlite"))
	if err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	db, err := sql.Open("sqlite", filepath.ToSlash(paths[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// This connection is only for test injection; unlike Store's writer it
	// does not own the production write gate. Wait for its short transactions.
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		t.Fatal(err)
	}
	// Materialize exactly the existing 30-minute expiry condition, without
	// changing authority epochs, user data, handles, or the tested code.
	if _, err = db.Exec("UPDATE navigation_cursors SET expires=0"); err != nil {
		t.Fatal(err)
	}
	response, body := f.request(t, "query", query, nil)
	t.Logf("after navigation expiry: status=%d body=%s", response.StatusCode, body)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("expired owner pagination must request refresh (409), got %d", response.StatusCode)
	}

	query.After = ""
	fresh := freshPage()
	query.After = fresh.Next
	if _, err = db.Exec("DELETE FROM navigation_cursors"); err != nil {
		t.Fatal(err)
	}
	response, _ = f.request(t, "query", query, nil)
	if response.StatusCode != http.StatusConflict {
		t.Fatal("reclaimed navigation did not require refresh", response.StatusCode)
	}

	query.After = ""
	freshPage()
}
