package authoritysubstrate

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

var testInitial = contract.ControlState{
	Schema: contract.ControlStateSchema, Revision: 1,
	ActiveComposition: strings.Repeat("a", 64), ActiveKernelGeneration: strings.Repeat("b", 64),
}

func TestControlStateInitializesOnceAndCASIsDurable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	first, err := Open(root, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, controlDirectory, controlFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	state := first.Control().ReadControl()
	next := state
	next.Revision++
	if _, err := first.Control().CompareAndSwapControl(0, next); err == nil {
		t.Fatal("stale control revision was accepted")
	}
	updated, err := first.Control().CompareAndSwapControl(state.Revision, next)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("control CAS failed: %#v %v", updated, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Control().ReadControl(); !reflect.DeepEqual(got, next) {
		t.Fatalf("control state did not survive cold open: %#v", got)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		return
	}
	// The only durable rewrite was the explicit successful CAS; cold open did
	// not add another revision or a second state representation.
	decoded, err := decodeControl(after)
	if err != nil || decoded.Revision != 2 {
		t.Fatalf("unexpected repeated initialization: %#v %v", decoded, err)
	}
}

func TestControlStateCASAllowsOnlyOneConcurrentDecision(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	substrate, err := Open(root, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	defer substrate.Close()
	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for index := 0; index < 2; index++ {
		go func() {
			ready.Done()
			<-start
			next := contract.ControlState{
				Schema: contract.ControlStateSchema, Revision: 2,
				ActiveComposition: testInitial.ActiveComposition, ActiveKernelGeneration: testInitial.ActiveKernelGeneration,
			}
			_, err := substrate.Control().CompareAndSwapControl(1, next)
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	successes := 0
	for index := 0; index < 2; index++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 || substrate.Control().ReadControl().Revision != 2 {
		t.Fatalf("concurrent CAS successes=%d state=%#v", successes, substrate.Control().ReadControl())
	}
}

func TestControlStateRejectsTruncationTamperAndConcurrentOpen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	first, err := Open(root, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, testInitial); err == nil {
		t.Fatal("concurrent authority owner was accepted")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, controlDirectory, controlFile)
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope controlEnvelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.State.ActiveKernelGeneration = strings.Repeat("c", 64)
	tampered, _ := json.Marshal(envelope)
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, testInitial); err == nil || !strings.Contains(err.Error(), "完整性") {
		t.Fatalf("tampered control was accepted: %v", err)
	}
	if err := os.WriteFile(path, encoded[:len(encoded)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, testInitial); err == nil {
		t.Fatal("truncated control was accepted")
	}
}

func TestBackupRestoresAuthorityAndLeavesDerivedAndRuntimeOut(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	substrate, err := Open(source, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 1, 2, 3, 0, time.UTC)
	value := domain.Information{Schema: domain.AssetSchema, ID: "asset-1", Revision: 1, CreatedAt: now, UpdatedAt: now, Kind: domain.KindKnowledge, Content: "权威备份必须同时覆盖资产和最小控制状态。"}
	if _, err := substrate.Assets().CreateAsset(value); err != nil {
		t.Fatal(err)
	}
	state := substrate.Control().ReadControl()
	state.Revision++
	if _, err := substrate.Control().CompareAndSwapControl(1, state); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"state/derived.tmp", "runtime/service.pid"} {
		path := filepath.Join(source, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("disposable"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backup := filepath.Join(root, "authority.ownward")
	if err := substrate.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if err := substrate.Close(); err != nil {
		t.Fatal(err)
	}
	restoredDir := filepath.Join(root, "restored")
	if err := os.Mkdir(restoredDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Restore(backup, restoredDir, testInitial); err != nil {
		t.Fatal(err)
	}
	if err := Restore(backup, restoredDir, testInitial); err != nil {
		t.Fatalf("identical repeated restore was not idempotent: %v", err)
	}
	restored, err := Open(restoredDir, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	actual, exists := restored.Assets().ReadCurrent(value.ID)
	if !exists || !reflect.DeepEqual(actual, value) || restored.Control().ReadControl().Revision != 2 {
		t.Fatalf("authority restore mismatch: %#v %#v", actual, restored.Control().ReadControl())
	}
	for _, relative := range []string{"state", "runtime"} {
		if _, err := os.Stat(filepath.Join(restoredDir, relative)); !os.IsNotExist(err) {
			t.Fatalf("%s state leaked into authority backup", relative)
		}
	}
}

func TestLargeAuthorityBackupUsesBoundedStreaming(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	substrate, err := Open(source, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	defer substrate.Close()
	now := time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC)
	value := domain.Information{
		Schema: domain.AssetSchema, ID: "large-asset", Revision: 1, CreatedAt: now, UpdatedAt: now,
		Kind: domain.KindKnowledge, Content: strings.Repeat("有界流式权威正文-0123456789abcdef", 300000),
	}
	if _, err := substrate.Assets().CreateAsset(value); err != nil {
		t.Fatal(err)
	}
	original := writeAssetSnapshot
	defer func() { writeAssetSnapshot = original }()
	var maximumWrite int
	writeAssetSnapshot = func(store *assetlog.Store, destination io.Writer) error {
		return store.WriteBackup(writerObserver{destination: destination, maximum: &maximumWrite})
	}
	backup := filepath.Join(root, "large-authority.ownward")
	if err := substrate.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if maximumWrite > 64*1024 {
		t.Fatalf("large asset backup emitted an unbounded write: %d bytes", maximumWrite)
	}
	restored := filepath.Join(root, "restored")
	if err := Restore(backup, restored, testInitial); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(restored, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	actual, ok := opened.Assets().ReadCurrent(value.ID)
	if !ok || actual.Content != value.Content {
		t.Fatal("large authority asset did not survive streaming backup and restore")
	}
}

type writerObserver struct {
	destination io.Writer
	maximum     *int
}

func (w writerObserver) Write(content []byte) (int, error) {
	if len(content) > *w.maximum {
		*w.maximum = len(content)
	}
	return w.destination.Write(content)
}

func TestBackupRetriesWhenControlRevisionChanges(t *testing.T) {
	root := t.TempDir()
	substrate, err := Open(filepath.Join(root, "source"), testInitial)
	if err != nil {
		t.Fatal(err)
	}
	defer substrate.Close()
	original := writeAssetSnapshot
	defer func() { writeAssetSnapshot = original }()
	calls := 0
	writeAssetSnapshot = func(store *assetlog.Store, destination io.Writer) error {
		calls++
		if calls == 1 {
			current := substrate.Control().ReadControl()
			next := current
			next.Revision++
			if _, err := substrate.Control().CompareAndSwapControl(current.Revision, next); err != nil {
				return err
			}
		}
		return original(store, destination)
	}
	backup := filepath.Join(root, "consistent.ownward")
	if err := substrate.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("control revision change did not force one complete snapshot retry: calls=%d", calls)
	}
	restored := filepath.Join(root, "restored")
	if err := Restore(backup, restored, testInitial); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(restored, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if revision := opened.Control().ReadControl().Revision; revision != 2 {
		t.Fatalf("backup combined assets with stale control revision %d", revision)
	}
}

func TestRestoreRejectsTargetsContainingDerivedRuntimeOrOtherState(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	substrate, err := Open(source, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "authority.ownward")
	if err := substrate.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if err := substrate.Close(); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"state/derived.json", "runtime/service.pid", "other/user.data"} {
		t.Run(relative, func(t *testing.T) {
			destination := filepath.Join(root, strings.ReplaceAll(relative, "/", "-"))
			marker := filepath.Join(destination, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := Restore(backup, destination, testInitial); err == nil {
				t.Fatal("restore accepted a target containing non-authority state")
			}
			content, err := os.ReadFile(marker)
			if err != nil || string(content) != "preserve" {
				t.Fatalf("restore changed rejected target content: %q %v", content, err)
			}
		})
	}
	different := filepath.Join(root, "different-authority")
	if err := Restore(backup, different, testInitial); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(different, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC)
	value := domain.Information{Schema: domain.AssetSchema, ID: "different", Revision: 1, CreatedAt: now, UpdatedAt: now, Kind: domain.KindGeneral, Content: "不同权威状态不得被静默替换。"}
	if _, err := opened.Assets().CreateAsset(value); err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(backup, different, testInitial); err == nil {
		t.Fatal("restore replaced a different authority state")
	}
	opened, err = Open(different, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if _, ok := opened.Assets().ReadCurrent(value.ID); !ok {
		t.Fatal("rejected restore changed the different authority state")
	}
}

func TestRestoreInstallFailureLeavesNoPartialAuthority(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	substrate, err := Open(source, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "authority.ownward")
	if err := substrate.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if err := substrate.Close(); err != nil {
		t.Fatal(err)
	}
	original := installDirectory
	defer func() { installDirectory = original }()
	sentinel := errors.New("injected install failure")
	installDirectory = func(string, string) error { return sentinel }
	destination := filepath.Join(root, "restored")
	if err := Restore(backup, destination, testInitial); !errors.Is(err, sentinel) {
		t.Fatalf("expected injected install failure, got %v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed single-switch restore left a product-openable partial root: %v", err)
	}
	staging, err := filepath.Glob(filepath.Join(root, ".authority-restore-*"))
	if err != nil || len(staging) != 0 {
		t.Fatalf("failed restore left staging state: %v %v", staging, err)
	}
}

func TestLegacyAssetBackupMigratesIdempotently(t *testing.T) {
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "legacy-assets"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := domain.Information{Schema: domain.AssetSchema, ID: "legacy", Revision: 1, CreatedAt: now, UpdatedAt: now, Kind: domain.KindGeneral, Content: "旧备份无损迁移。"}
	if err := assets.Create(value); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "legacy.ownward")
	if err := assets.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if err := assets.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "restored")
	if err := Restore(backup, destination, testInitial); err != nil {
		t.Fatal(err)
	}
	if err := Restore(backup, destination, testInitial); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(destination, testInitial)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if got, ok := opened.Assets().ReadCurrent(value.ID); !ok || got.Content != value.Content {
		t.Fatalf("legacy asset was not restored: %#v", got)
	}
	if got := opened.Control().ReadControl(); got.Revision != 1 || got.Schema != contract.ControlStateSchema {
		t.Fatalf("legacy restore did not initialize control state: %#v", got)
	}
}
