package boundedstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func stopMaintenance(s *Store) { s.maintenanceCancel(); <-s.maintenanceDone }
func drainTest(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := s.DrainMaintenance(ctx); e != nil {
		t.Fatal(e)
	}
}
func countTest(t *testing.T, s *Store, q string, args ...any) int64 {
	t.Helper()
	var n int64
	e := s.view(context.Background(), func(c queryer) error { return c.QueryRowContext(context.Background(), q, args...).Scan(&n) })
	if e != nil {
		t.Fatal(e)
	}
	return n
}

func TestMaintenanceReclaimsRevisionsAndInterruptedStages(t *testing.T) {
	s := retrievalStore(t)
	ctx := context.Background()
	path := s.path
	started := time.Now()
	var high int64
	for i := 1; i <= 40; i++ {
		text := fmt.Sprintf("revision-%d ", i) + strings.Repeat("bounded raw material ", 12000)
		putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: uint64(i), Content: text})
		if s.walBytes() > high {
			high = s.walBytes()
		}
	}
	drainTest(t, s)
	if n := countTest(t, s, "SELECT count(*) FROM payloads"); n != 1 {
		t.Fatalf("old payloads retained: %d", n)
	}
	if n := countTest(t, s, "SELECT value FROM store_meta WHERE key='reclaim_bytes'"); n != 0 {
		t.Fatalf("backlog %d", n)
	}
	if got := readTest(t, s, "a"); !strings.HasPrefix(got, "revision-40 ") {
		t.Fatal("current data changed")
	}
	stopMaintenance(s)
	if _, e := s.Stage(ctx, "interrupted", StringSource(strings.Repeat("orphan", 30000)), nil); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	s = openTest(t, path)
	drainTest(t, s)
	if n := countTest(t, s, "SELECT count(*) FROM payloads"); n != 1 {
		t.Fatalf("orphan survived restart: %d", n)
	}
	stat, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	if stat.Size() > 12*resourcebudget.MiB {
		t.Fatalf("revision count inflated database: %d", stat.Size())
	}
	t.Logf("40 revisions: elapsed=%s main_bytes=%d peak_sampled_wal=%d", time.Since(started), stat.Size(), high)
}

func TestForgetBarrierCleanupAndLatePublication(t *testing.T) {
	s := retrievalStore(t)
	ctx := context.Background()
	stopMaintenance(s)
	if e := s.CreateGeneration(ctx, "g", "space"); e != nil {
		t.Fatal(e)
	}
	marker := "PRIVATE_REMOVAL_MARKER_720193"
	for _, id := range []string{"a", "b", "c"} {
		putRetrievalAsset(t, s, domain.Information{ID: id, Revision: 1, Content: id + " " + marker})
	}
	for _, id := range []string{"a", "b", "c"} {
		r := derived.Record{AssetID: id, AssetRevision: 1, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: "summary " + id}}
		if id == "b" {
			r.Analysis.Relations = []semantics.Relation{{Type: "supports", TargetID: "a", TargetRevision: 1, Evidence: marker}}
		}
		putRecord(t, s, r)
	}
	if e := s.ActivateGeneration(ctx, "g", ""); e != nil {
		t.Fatal(e)
	}
	if e := s.StageForget(ctx, "forget-a", []ForgetTarget{{"a", 1}}); e != nil {
		t.Fatal(e)
	}
	if e := s.CommitForget(ctx, "forget-a"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ReadAssetMeta(ctx, "a", 0); !errors.Is(e, ErrNotFound) {
		t.Fatal("barrier not immediate", e)
	}
	if _, e := s.CurrentOrganization(ctx, "g", "b"); e == nil {
		t.Fatal("contaminated organization visible before cleanup")
	}
	if _, e := s.CurrentOrganization(ctx, "g", "c"); e != nil {
		t.Fatal("unrelated organization lost", e)
	}
	if n := countTest(t, s, "SELECT documents FROM lexical_stats"); n != 2 {
		t.Fatal("forgotten document still affects scoring", n)
	}
	drainTest(t, s)
	state, e := s.ForgetState(ctx, "forget-a")
	if e != nil || state != "complete" {
		t.Fatal(state, e)
	}
	if n := countTest(t, s, "SELECT count(*) FROM organizations WHERE asset='a' OR asset='b'"); n != 0 {
		t.Fatal("contaminated bodies survived", n)
	}
	if got := readTest(t, s, "b"); got != "b "+marker {
		t.Fatal("unforgotten source changed")
	}
	v := stageTest(t, s, operation("resurrect"), "a", "late", 1)
	if e := s.Publish(ctx, receipt(operation("resurrect"), v), []AssetWrite{v}); e == nil {
		t.Fatal("deleted identity recreated")
	}
	if e := s.Abandon(ctx, v.Payload); e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	// Forget all remaining instances so a byte scan can distinguish actual copies.
	if e := s.StageForget(ctx, "forget-rest", []ForgetTarget{{"b", 1}, {"c", 1}}); e != nil {
		t.Fatal(e)
	}
	if e := s.CommitForget(ctx, "forget-rest"); e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	path := s.path
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	for _, suffix := range []string{"", "-wal"} {
		b, e := os.ReadFile(path + suffix)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			t.Fatal(e)
		}
		if bytes.Contains(b, []byte(marker)) {
			t.Fatal("forgotten text remains in controlled database", suffix)
		}
	}
	s = openTest(t, path)
	if _, e := s.ReadAssetMeta(ctx, "a", 0); !errors.Is(e, ErrNotFound) {
		t.Fatal("restart resurrected source", e)
	}
}

func TestSnapshotRestoreKeepsDeletionAndInvalidatesCredentials(t *testing.T) {
	s := retrievalStore(t)
	ctx := context.Background()
	p := contract.Principal{ID: "owner", Revision: 1, CredentialDigest: strings.Repeat("a", 64), Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission, contract.ManagePermission}}
	if e := s.PublishAccess(ctx, AccessHeader{System: "system", Revision: 1}, 0, []contract.Principal{p}); e != nil {
		t.Fatal(e)
	}
	putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: 1, Content: "private backup original"})
	putRetrievalAsset(t, s, domain.Information{ID: "b", Revision: 1, Content: "keep exactly"})
	snapshot, e := s.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	// Simulate an older copy outside the controlled directory. Restore must overlay
	// the current deletion ledger even when that old file could not be erased.
	external := filepath.Join(t.TempDir(), "old.sqlite")
	raw, e := os.ReadFile(snapshot.Path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(external, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = s.StageForget(ctx, "delete-a", []ForgetTarget{{"a", 1}}); e != nil {
		t.Fatal(e)
	}
	if e = s.CommitForget(ctx, "delete-a"); e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	if _, e = os.Stat(snapshot.Path); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("controlled backup survived forgetting", e)
	}
	budget, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
	restored, e := s.RestoreSnapshot(ctx, StorageSnapshot{Path: external, SHA256: snapshot.SHA256}, filepath.Join(t.TempDir(), "restored.sqlite"), Options{Budget: budget})
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	if _, e = restored.ReadAssetMeta(ctx, "a", 0); !errors.Is(e, ErrNotFound) {
		t.Fatal("old backup resurrected deletion", e)
	}
	if got := readTest(t, restored, "b"); got != "keep exactly" {
		t.Fatal(got)
	}
	if n := countTest(t, restored, "SELECT documents FROM lexical_stats"); n != 1 {
		t.Fatal("restored lexical statistics include forgotten material", n)
	}
	if _, e = restored.BeginAccess(ctx, p.CredentialDigest, contract.ReadPermission); !errors.Is(e, ErrAccess) {
		t.Fatal("restored old credential active", e)
	}
	if n := countTest(t, restored, "SELECT count(*) FROM access_principals WHERE id='owner'"); n != 1 {
		t.Fatal("lost ownership identity")
	}
}

func TestInvalidationDuringStagingKeepsPublicationCAS(t *testing.T) {
	s := retrievalStore(t)
	stopMaintenance(s)
	ctx := context.Background()
	if e := s.CreateGeneration(ctx, "g", "space"); e != nil {
		t.Fatal(e)
	}
	putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: 1, Content: "before"})
	putRecord(t, s, derived.Record{AssetID: "a", AssetRevision: 1, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: "before"}})
	if e := s.ActivateGeneration(ctx, "g", ""); e != nil {
		t.Fatal(e)
	}
	putRetrievalAsset(t, s, domain.Information{ID: "a", Revision: 2, Content: "after"})
	r := derived.Record{AssetID: "a", AssetRevision: 2, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: "after"}}
	raw, _ := json.Marshal(r)
	first, e := s.StageOrganization(ctx, "g", StringSource(raw))
	if e != nil {
		t.Fatal(e)
	}
	rival, e := s.StageOrganization(ctx, "g", StringSource(raw))
	if e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	if e = s.PublishOrganization(ctx, first); e != nil {
		t.Fatal("reclamation invalidated a valid pending publication", e)
	}
	if e = s.PublishOrganization(ctx, rival); e == nil {
		t.Fatal("genuine competing publication was not rejected")
	}
	if current, e := s.CurrentOrganization(ctx, "g", "a"); e != nil || current.ID != first.ID {
		t.Fatal(current, e)
	}
	if e = s.CreateGeneration(ctx, "g2", "space"); e != nil {
		t.Fatal(e)
	}
	next, e := s.StageOrganization(ctx, "g2", StringSource(raw))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.PublishOrganization(ctx, next); e != nil {
		t.Fatal(e)
	}
	if e = s.ActivateGeneration(ctx, "g2", "g"); e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	if n := countTest(t, s, "SELECT count(*) FROM organizations WHERE generation='g'"); n != 0 {
		t.Fatal("retired generation bodies survived", n)
	}
	if n := countTest(t, s, "SELECT count(*) FROM organization_heads WHERE generation='g'"); n != 0 {
		t.Fatal("retired generation identities accumulated", n)
	}
	if current, e := s.CurrentOrganization(ctx, "g2", "a"); e != nil || current.ID != next.ID {
		t.Fatal(current, e)
	}
}

func TestWALControlReserveInterruptsPinnedSnapshot(t *testing.T) {
	s := retrievalStore(t)
	stopMaintenance(s)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, release, e := s.reader(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	tx, e := c.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM assets").Scan(&n); e != nil {
		t.Fatal(e)
	}
	if e = s.write(workContext(ctx, controlWork), func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "CREATE TABLE wal_probe(id INTEGER PRIMARY KEY,body BLOB)")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	var peak, amplification int64
	for i := 0; i < 72; i++ {
		before := s.walBytes()
		e = s.write(workContext(ctx, controlWork), func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, "INSERT INTO wal_probe(body) VALUES(?)", bytes.Repeat([]byte{byte(i)}, 512*1024))
			return e
		})
		if e != nil {
			t.Fatal(e)
		}
		after := s.walBytes()
		if after > peak {
			peak = after
		}
		if after-before > amplification {
			amplification = after - before
		}
	}
	if c.ctx.Err() == nil {
		t.Fatal("pinned read not cancelled at control reserve")
	}
	if peak > walControl {
		t.Fatalf("WAL reserve exceeded: %d", peak)
	}
	if amplification > walTransactionReserve {
		t.Fatalf("transaction reserve underestimated: %d", amplification)
	}
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM assets").Scan(&n); e == nil {
		t.Fatal("cancelled SQLite transaction remained usable")
	}
	t.Logf("pinned-reader continuous-control writes: peak_wal=%d largest_transaction_growth=%d", peak, amplification)
}

func TestWALOrdinaryAdmissionPreservesControlReserve(t *testing.T) {
	s := retrievalStore(t)
	stopMaintenance(s)
	ctx := context.Background()
	c, release, e := s.reader(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	tx, e := c.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM assets").Scan(&n); e != nil {
		t.Fatal(e)
	}
	control := workContext(ctx, controlWork)
	if e = s.write(control, func(tx *sql.Tx) error { _, e := tx.ExecContext(ctx, "CREATE TABLE wal_admission(body BLOB)"); return e }); e != nil {
		t.Fatal(e)
	}
	for s.walBytes()+walTransactionReserve < walOrdinary {
		if e = s.write(control, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, "INSERT INTO wal_admission VALUES(?)", bytes.Repeat([]byte("x"), 512*1024))
			return e
		}); e != nil {
			t.Fatal(e)
		}
	}
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	e = s.write(short, func(tx *sql.Tx) error { t.Error("ordinary growth crossed WAL barrier"); return nil })
	cancel()
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	short, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
	_, e = s.ReadAssetMeta(short, "absent", 0)
	cancel()
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal("new reader admitted behind WAL barrier", e)
	}
	if e = s.PublishAccess(ctx, AccessHeader{System: "system", Revision: 1}, 0, nil); e != nil {
		t.Fatal("control blocked by ordinary backlog", e)
	}
	if c.ctx.Err() != nil {
		t.Fatal("ordinary pressure cancelled existing reader")
	}
	tx.Rollback()
	release()
	drainTest(t, s)
	if s.walBytes() >= walOrdinary {
		t.Fatal("WAL failed to converge after snapshot release")
	}
}

func TestSnapshotRejectsCorruptionAndCancellation(t *testing.T) {
	s := retrievalStore(t)
	ctx := context.Background()
	if e := s.PublishAccess(ctx, AccessHeader{System: "system", Revision: 1}, 0, nil); e != nil {
		t.Fatal(e)
	}
	snapshot, e := s.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(snapshot.Path)
	if e != nil {
		t.Fatal(e)
	}
	b[len(b)-1] ^= 1
	damaged := filepath.Join(t.TempDir(), "damaged.sqlite")
	if e = os.WriteFile(damaged, b, 0600); e != nil {
		t.Fatal(e)
	}
	destination := filepath.Join(t.TempDir(), "restore.sqlite")
	if restored, e := s.RestoreSnapshot(ctx, StorageSnapshot{Path: damaged, SHA256: snapshot.SHA256}, destination, Options{Budget: s.budget}); e == nil {
		restored.Close()
		t.Fatal("corrupt snapshot accepted")
	}
	if _, e = os.Stat(destination); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("failed restore left visible destination", e)
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = s.Snapshot(stopped); !errors.Is(e, context.Canceled) {
		t.Fatal("cancelled backup proceeded", e)
	}
	drainTest(t, s)
	if n := countTest(t, s, "SELECT count(*) FROM controlled_copies WHERE state='building'"); n != 0 {
		t.Fatal("failed backup left work", n)
	}
}
