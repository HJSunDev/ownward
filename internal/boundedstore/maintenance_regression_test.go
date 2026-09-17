package boundedstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestMaintenanceSharedVectorForgetAcrossRestart(t *testing.T) {
	for _, packed := range []bool{false, true} {
		name := "delta"
		if packed {
			name = "packed"
		}
		t.Run(name, func(t *testing.T) {
			s := retrievalStore(t)
			stopMaintenance(s)
			ctx := context.Background()
			if e := s.CreateGeneration(ctx, "g", "space"); e != nil {
				t.Fatal(e)
			}
			vectors := make([][]float32, 5)
			for i, id := range []string{"a", "b", "c", "d", "e"} {
				putRetrievalAsset(t, s, domain.Information{ID: id, Revision: 1, Content: "source " + id})
				vectors[i] = make([]float32, 512)
				vectors[i][i] = 1
				putRecord(t, s, derived.Record{AssetID: id, AssetRevision: 1, Status: "ready", InputsKnown: true, EmbeddingSpace: "space", Embedding: vectors[i], Analysis: semantics.Analysis{Summary: "source " + id}})
			}
			if e := s.ActivateGeneration(ctx, "g", ""); e != nil {
				t.Fatal(e)
			}
			if packed {
				if n, e := s.PackVectors(ctx, "space", true); e != nil || n != 5 {
					t.Fatal(n, e)
				}
			}
			drainTest(t, s)
			before, _, e := s.VectorSearch(ctx, "g", "space", vectors[1], nil, 10)
			if e != nil {
				t.Fatal(e)
			}
			if e = s.StageForget(ctx, "forget-shared", []ForgetTarget{{"a", 1}}); e != nil {
				t.Fatal(e)
			}
			if e = s.CommitForget(ctx, "forget-shared"); e != nil {
				t.Fatal(e)
			}
			if packed {
				for i := 0; i < 64 && countTest(t, s, "SELECT count(*) FROM derived_reclaim WHERE kind='vector_filter'") == 0; i++ {
					if _, e = s.Maintain(ctx); e != nil {
						t.Fatal(e)
					}
				}
				if countTest(t, s, "SELECT count(*) FROM derived_reclaim WHERE kind='vector_filter'") != 1 {
					t.Fatal("shared block cleanup not durably queued")
				}
			}
			path, budget := s.path, s.budget
			if e = s.Close(); e != nil {
				t.Fatal(e)
			}
			s, e = Open(ctx, path, Options{Budget: budget})
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			drainTest(t, s)
			state, e := s.ForgetState(ctx, "forget-shared")
			if e != nil || state != "complete" {
				t.Fatal(state, e)
			}
			if countTest(t, s, "SELECT count(*) FROM vector_blocks") != 0 || countTest(t, s, "SELECT count(*) FROM vector_filter_pages") != 0 {
				t.Fatal("old shared filter retained")
			}
			if countTest(t, s, "SELECT count(*) FROM vectors") != 4 {
				t.Fatal("surviving exact vectors lost")
			}
			if _, e = s.PackVectors(ctx, "space", true); e != nil {
				t.Fatal(e)
			}
			after, _, e := s.VectorSearch(ctx, "g", "space", vectors[1], nil, 10)
			if e != nil {
				t.Fatal(e)
			}
			expected := before[:0]
			for _, hit := range before {
				if hit.ID != "a" {
					expected = append(expected, hit)
				}
			}
			if len(expected) != len(after) {
				t.Fatalf("surviving results: %v != %v", expected, after)
			}
			for i := range expected {
				if !reflect.DeepEqual(expected[i], after[i]) {
					t.Fatalf("surviving scores/order: %v != %v", expected, after)
				}
			}
		})
	}
}

func TestMaintenanceCancelledOrganizationRemainsRetryable(t *testing.T) {
	s := retrievalStore(t)
	stopMaintenance(s)
	ctx := context.Background()
	if e := s.CreateGeneration(ctx, "g", "space"); e != nil {
		t.Fatal(e)
	}
	putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: 1, Content: "source"})
	data, _ := json.Marshal(derived.Record{AssetID: "a", AssetRevision: 1, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: "source"}})
	v, e := s.StageOrganization(ctx, "g", StringSource(data))
	if e != nil {
		t.Fatal(e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if e = s.PublishOrganization(cancelled, v); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	drainTest(t, s)
	if countTest(t, s, "SELECT count(*) FROM organizations WHERE id=? AND state='ready'", v.ID) != 1 {
		t.Fatal("valid retryable candidate reclaimed")
	}
	if e = s.PublishOrganization(ctx, v); e != nil {
		t.Fatal(e)
	}
}

func TestMaintenanceControlPreservesBothSchedulerTurns(t *testing.T) {
	for _, previous := range []workClass{maintenanceWork, ordinaryWork} {
		name := "after-maintenance"
		if previous == ordinaryWork {
			name = "after-ordinary"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var gate writeGate
			if e := gate.acquire(ctx, previous); e != nil {
				t.Fatal(e)
			}
			stopped, stop := context.WithCancel(ctx)
			stop()
			if e := gate.acquire(stopped, ordinaryWork); !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
			acquired := make(chan workClass, 3)
			releases := map[workClass]chan struct{}{ordinaryWork: make(chan struct{}), maintenanceWork: make(chan struct{}), controlWork: make(chan struct{})}
			done := make(chan struct{}, 3)
			for _, class := range []workClass{ordinaryWork, maintenanceWork, controlWork} {
				go func(class workClass) {
					defer func() { done <- struct{}{} }()
					if e := gate.acquire(ctx, class); e != nil {
						return
					}
					acquired <- class
					select {
					case <-releases[class]:
					case <-ctx.Done():
					}
					gate.Unlock()
				}(class)
			}
			for {
				gate.mu.Lock()
				queued := gate.waiting[0] == 1 && gate.waiting[1] == 1 && gate.waiting[2] == 1
				gate.mu.Unlock()
				if queued {
					break
				}
				select {
				case <-ctx.Done():
					gate.Unlock()
					t.Fatal("waiters not queued")
				case <-time.After(time.Millisecond):
				}
			}
			gate.Unlock()
			order := make([]workClass, 0, 3)
			for range 3 {
				select {
				case class := <-acquired:
					order = append(order, class)
					close(releases[class])
				case <-ctx.Done():
					t.Fatal("scheduler did not progress")
				}
			}
			for range 3 {
				<-done
			}
			expected := ordinaryWork
			if previous == ordinaryWork {
				expected = maintenanceWork
			}
			if order[0] != controlWork || order[1] != expected {
				t.Fatalf("previous=%v order=%v", previous, order)
			}
		})
	}
}
func TestMaintenanceForgetRemovesPackedVectorMaterial(t *testing.T) {
	s := retrievalStore(t)
	stopMaintenance(s)
	ctx := context.Background()
	if e := s.CreateGeneration(ctx, "g", "space"); e != nil {
		t.Fatal(e)
	}
	putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: 1, Content: "one source to forget"})
	vector := make([]float32, 512)
	for i := range vector {
		vector[i] = float32(i+1) / 1024
	}
	putRecord(t, s, derived.Record{AssetID: "a", AssetRevision: 1, Status: "ready", InputsKnown: true, EmbeddingSpace: "space", Embedding: vector, Analysis: semantics.Analysis{Summary: "one source"}})
	if e := s.ActivateGeneration(ctx, "g", ""); e != nil {
		t.Fatal(e)
	}
	if n, e := s.PackVectors(ctx, "space", true); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	drainTest(t, s)
	if n := countTest(t, s, "SELECT count(*) FROM derived_reclaim"); n != 0 {
		t.Fatal("baseline queue not drained", n)
	}
	if e := s.StageForget(ctx, "forget-vector", []ForgetTarget{{"a", 1}}); e != nil {
		t.Fatal(e)
	}
	if e := s.CommitForget(ctx, "forget-vector"); e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	state, e := s.ForgetState(ctx, "forget-vector")
	if e != nil {
		t.Fatal(e)
	}
	blocks := countTest(t, s, "SELECT count(*) FROM vector_blocks")
	pages := countTest(t, s, "SELECT count(*) FROM vector_filter_pages")
	vectors := countTest(t, s, "SELECT count(*) FROM vectors")
	reconstructs := false
	if blocks > 0 {
		var data []byte
		if e = s.view(ctx, func(q queryer) error {
			return q.QueryRowContext(ctx, "SELECT header FROM vector_blocks LIMIT 1").Scan(&data)
		}); e != nil {
			t.Fatal(e)
		}
		var header vectorHeader
		if e = json.Unmarshal(data, &header); e != nil {
			t.Fatal(e)
		}
		reconstructs = true
		for i, x := range normalizeVector(vector) {
			reconstructs = reconstructs && header.Center[i] == float64(x) && header.Lo[i] == float64(x)
		}
	}
	t.Logf("forget_state=%s raw_vectors=%d blocks=%d filter_pages=%d retained_header_exactly_reconstructs_vector=%v", state, vectors, blocks, pages, reconstructs)
	if state == "complete" && (blocks != 0 || pages != 0) {
		t.Errorf("forget reported complete with packed derived material still present")
	}
}

func TestMaintenanceAuthenticatedSnapshotRestore(t *testing.T) {
	s := retrievalStore(t)
	ctx := context.Background()
	principal := contract.Principal{ID: "review-owner", Revision: 1, CredentialDigest: strings.Repeat("b", 64), Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission, contract.ManagePermission}}
	if e := s.PublishAccess(ctx, AccessHeader{System: "review-system", Revision: 1}, 0, []contract.Principal{principal}); e != nil {
		t.Fatal(e)
	}
	putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: 1, Content: "restore retained source"})
	authenticated, e := s.BeginAccess(contract.WithAuthenticationDigest(ctx, principal.CredentialDigest), principal.CredentialDigest, contract.ManagePermission)
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := s.Snapshot(authenticated)
	if e != nil {
		t.Fatal(e)
	}
	restoreCtx, cancel := context.WithTimeout(authenticated, 10*time.Second)
	defer cancel()
	b, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
	restored, e := s.RestoreSnapshot(restoreCtx, snapshot, filepath.Join(t.TempDir(), "authenticated.sqlite"), Options{Budget: b})
	if restored != nil {
		restored.Close()
	}
	t.Logf("authenticated_restore_error=%v is_access_error=%v", e, errors.Is(e, ErrAccess))
	if e != nil {
		t.Errorf("valid source manager could not complete restore: %v", e)
	}
	controlBudget, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
	control, e := s.RestoreSnapshot(ctx, snapshot, filepath.Join(t.TempDir(), "trusted-control.sqlite"), Options{Budget: controlBudget})
	if e != nil {
		t.Fatal("trusted-context control also failed", e)
	}
	defer control.Close()
	if got := readTest(t, control, "a"); got != "restore retained source" {
		t.Fatal("control source mismatch")
	}
	if _, e = control.BeginAccess(ctx, principal.CredentialDigest, contract.ReadPermission); !errors.Is(e, ErrAccess) {
		t.Fatal("old credential must stay invalid", e)
	}
	t.Log("same snapshot succeeds via trusted context and old credentials remain invalid")
}

func TestMaintenanceRejectedOrganizationGetsReclaimed(t *testing.T) {
	s := retrievalStore(t)
	stopMaintenance(s)
	ctx := context.Background()
	if e := s.CreateGeneration(ctx, "g", "space"); e != nil {
		t.Fatal(e)
	}
	putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: 1, Content: "current source"})
	raw, _ := json.Marshal(derived.Record{AssetID: "a", AssetRevision: 1, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: strings.Repeat("retained abandoned candidate ", 5000)}})
	first, e := s.StageOrganization(ctx, "g", StringSource(raw))
	if e != nil {
		t.Fatal(e)
	}
	second, e := s.StageOrganization(ctx, "g", StringSource(raw))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.PublishOrganization(ctx, first); e != nil {
		t.Fatal(e)
	}
	rejection := s.PublishOrganization(ctx, second)
	if rejection == nil {
		t.Fatal("rival was not rejected")
	}
	if countTest(t, s, "SELECT count(*) FROM derived_reclaim WHERE id=?", second.ID) != 1 {
		t.Fatal("permanent rejection did not durably queue cleanup")
	}
	path, budget := s.path, s.budget
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(ctx, path, Options{Budget: budget})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	drainTest(t, s)
	left := countTest(t, s, "SELECT count(*) FROM organizations WHERE id=?", second.ID)
	bytes := countTest(t, s, "SELECT coalesce(sum(length(bytes)),0) FROM organization_chunks WHERE organization=?", second.ID)
	queue := countTest(t, s, "SELECT count(*) FROM derived_reclaim WHERE id=?", second.ID)
	t.Logf("rejection=%v rejected_versions=%d retained_body_bytes=%d reclaim_jobs=%d", rejection, left, bytes, queue)
	if left != 0 {
		t.Errorf("permanently rejected candidate was not reclaimed during normal maintenance")
	}
	current, e := s.CurrentOrganization(ctx, "g", "a")
	if e != nil || current.ID != first.ID {
		t.Fatal("current winner damaged", e)
	}
}

func TestMaintenanceRestoreStagedForgetKeepsLexicalStatistics(t *testing.T) {
	s := retrievalStore(t)
	ctx := context.Background()
	if e := s.PublishAccess(ctx, AccessHeader{System: "staged-forget-system", Revision: 1}, 0, nil); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"a", "b"} {
		putRetrievalAsset(t, s, domain.Information{ID: id, Revision: 1, Content: "source " + id})
	}
	if e := s.StageForget(ctx, "staged-then-committed", []ForgetTarget{{"a", 1}}); e != nil {
		t.Fatal(e)
	}
	snapshot, e := s.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(snapshot.Path)
	if e != nil {
		t.Fatal(e)
	}
	external := filepath.Join(t.TempDir(), "old-staged.sqlite")
	if e = os.WriteFile(external, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = s.CommitForget(ctx, "staged-then-committed"); e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	b, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
	restored, e := s.RestoreSnapshot(ctx, StorageSnapshot{Path: external, SHA256: snapshot.SHA256}, filepath.Join(t.TempDir(), "restored.sqlite"), Options{Budget: b})
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	live := countTest(t, restored, "SELECT count(*) FROM live_assets")
	stats := countTest(t, restored, "SELECT documents FROM lexical_stats")
	terms := countTest(t, restored, "SELECT terms FROM lexical_stats")
	actualTerms := countTest(t, restored, "SELECT coalesce(sum(d.length),0) FROM lexical_documents d JOIN live_assets a ON a.payload=d.payload")
	t.Logf("restored_live_assets=%d lexical_documents_stat=%d actual_terms=%d terms_stat=%d", live, stats, actualTerms, terms)
	if live != 1 || stats != live || terms != actualTerms {
		t.Error("restoring a snapshot with staged forget targets corrupts retrieval corpus statistics")
	}
}
