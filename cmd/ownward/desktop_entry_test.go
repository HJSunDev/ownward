package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/desktop"
)

func TestDesktopDetailsDoNotRepeatFailedOperation(t *testing.T) {
	attempts, prompts, details := 0, 0, 0
	problem := &entryProblem{message: "请先完成迁移。", cause: errors.New("test diagnostic")}
	err := runDesktopLoop(context.Background(), func(context.Context) error {
		attempts++
		if attempts == 1 {
			return problem
		}
		return nil
	}, func(_, message, _, _ string) (int, error) {
		if attempts != 1 || message != problem.message {
			t.Fatal("details restarted work or hid the next step")
		}
		prompts++
		if prompts <= 2 {
			return 2, nil
		}
		return 1, nil
	}, func(message string) error {
		details++
		if message != problem.Error() {
			t.Fatal("lost diagnostic")
		}
		return nil
	})
	if err != nil || attempts != 2 || details != 2 || prompts != 3 {
		t.Fatal(attempts, prompts, details, err)
	}
	for _, cancelOperation := range []bool{false, true} {
		prompts = 0
		err = runDesktopLoop(context.Background(), func(context.Context) error {
			if cancelOperation {
				return context.Canceled
			}
			return problem
		}, func(string, string, string, string) (int, error) { prompts++; return 0, nil }, func(string) error { t.Fatal("unexpected detail"); return nil })
		if err != nil || (cancelOperation && prompts != 0) || (!cancelOperation && prompts != 1) {
			t.Fatal("cancel did not end the flow", prompts, err)
		}
	}
}

func TestOwnerEntryNeverInitializesMissingOrUnfinishedData(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := openOwnerWindow(context.Background(), missing, "unused", io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "资料所在位置") {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("opening created a substitute library", err)
	}
	root := t.TempDir()
	a, err := authoritysubstrate.Open(root, contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = openOwnerWindow(context.Background(), root, "unused", io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "首次启用") {
		t.Fatal(err)
	}
	f := ownerHTTP(t)
	if state, err := readOwnerEntry(f.ctx, f.root); err != nil || state.InformationControl.SystemID != f.c.SystemID() {
		t.Fatal("active writer blocked identity check", err)
	}
}

func TestDesktopBindingUsesShownRevisionAndPreservesOtherSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.json")
	original := desktop.Binding{System: "original", Data: t.TempDir()}
	next := desktop.Binding{System: "restored", Data: t.TempDir()}
	state := desktop.State{Revision: 7, Release: "release", Executable: filepath.Join(t.TempDir(), "ownward"), Binding: &original}
	if err := desktop.Save(path, state); err != nil {
		t.Fatal(err)
	}
	if err := updateDesktopBinding(path, 6, original, next); err == nil {
		t.Fatal("stale confirmation accepted")
	}
	if err := updateDesktopBinding(path, 7, original, next); err != nil {
		t.Fatal(err)
	}
	if err := updateDesktopBinding(path, 7, original, original); err == nil {
		t.Fatal("old view replaced the new default")
	}
	after, err := desktop.Load(path)
	if err != nil || after.Revision != 8 || after.Binding.System != "restored" {
		t.Fatal(after, err)
	}
}

func TestNewDesktopDataCannotReplaceUnknownContents(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "desktop.json")
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkNewDataDirectory(root, state); err == nil {
		t.Fatal("initialized over unrecognized existing content")
	}
	if err := checkNewDataDirectory(filepath.Join(root, "new"), state); err != nil {
		t.Fatal(err)
	}
}

func TestInitializationResumeRequiresOriginalIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.json")
	data := t.TempDir()
	if pending, err := pendingInitialization(path, data, "original"); err != nil || pending {
		t.Fatal(pending, err)
	}
	b, _ := json.Marshal(initializationIntent{Schema: "ownward.initialize/v1", Data: data, Composition: "original"})
	if err := os.WriteFile(path+".preparing", b, 0600); err != nil {
		t.Fatal(err)
	}
	if pending, err := pendingInitialization(path, data, "original"); err != nil || !pending {
		t.Fatal(pending, err)
	}
	if _, err := pendingInitialization(path, data, "other-version"); err == nil {
		t.Fatal("changed composition resumed initialization")
	}
	if _, err := pendingInitialization(path, t.TempDir(), "original"); err == nil {
		t.Fatal("changed directory resumed initialization")
	}
}

func TestRestoreResultsResumeAndSwitchOnlyAfterVerifiedOpen(t *testing.T) {
	f := ownerHTTP(t)
	_, archive := f.request(t, "backup", struct{}{}, nil)
	archives := localOwnerArchives(f.k, f.c, f.root)
	first, err := archives.Restore(f.ctx, bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	second, err := archives.Restore(f.ctx, bytes.NewReader(archive))
	if err != nil || first != second {
		t.Fatal("retry duplicated restored data", first, second, err)
	}
	entries, err := restoreEntries(f.root, f.c.SystemID())
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	id := entries[0].ID
	settingsPath := filepath.Join(t.TempDir(), "desktop.json")
	settings := desktop.State{Revision: 3, Release: "release", Executable: filepath.Join(t.TempDir(), "ownward"), Binding: &desktop.Binding{System: f.c.SystemID(), Data: f.root}}
	if err = desktop.Save(settingsPath, settings); err != nil {
		t.Fatal(err)
	}
	opened := ""
	open := func(_ context.Context, target string) error { opened = target; return nil }
	if _, err = resumeRestoreAt(f.ctx, f.c, f.root, id, "default", 3, settingsPath, open); err == nil {
		t.Fatal("default changed before verified open")
	}
	if _, err = resumeRestoreAt(context.Background(), f.c, f.root, id, "open", 0, settingsPath, open); err == nil {
		t.Fatal("unauthenticated resume")
	}
	if opened != "" {
		t.Fatal("untrusted request reached opener")
	}
	result, err := resumeRestoreAt(f.ctx, f.c, f.root, id, "open", 0, settingsPath, open)
	if err != nil || opened != first || !result.(map[string]any)["default_available"].(bool) {
		t.Fatal(result, err, opened)
	}
	if _, err = resumeRestoreAt(f.ctx, f.c, f.root, id, "default", 2, settingsPath, open); err == nil {
		t.Fatal("stale switch")
	}
	if _, err = resumeRestoreAt(f.ctx, f.c, f.root, id, "default", 3, settingsPath, open); err != nil {
		t.Fatal(err)
	}
	after, err := desktop.Load(settingsPath)
	if err != nil || after.Revision != 4 || after.Binding.Data != first {
		t.Fatal(after, err)
	}
	result, err = resumeRestoreAt(f.ctx, f.c, f.root, id, "open", 0, filepath.Join(t.TempDir(), "missing.json"), open)
	if err != nil || result.(map[string]any)["default_available"] != false {
		t.Fatal("successful open depended on installed default", result, err)
	}
	// Lost final metadata write is recoverable from the durable restored target.
	r := entries[0]
	r.Ready = false
	r.System = ""
	if err = saveRestoreRecord(f.root, r); err != nil {
		t.Fatal(err)
	}
	reconciled, err := reconcileRestore(f.ctx, f.root, r)
	if err != nil || !reconciled.Ready || reconciled.System == "" {
		t.Fatal(reconciled, err)
	}
	for _, fake := range []string{"../anything", strings.Repeat("f", 48)} {
		if _, err = resumeRestoreAt(f.ctx, f.c, f.root, fake, "open", 0, settingsPath, open); err == nil {
			t.Fatal("forged restore result accepted")
		}
	}
}

func TestRestoreHistoryIsBoundedWithoutRemovingData(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < restoreHistoryLimit+2; i++ {
		r := restoredEntry{ID: connectionID(), Source: "source", Created: time.Unix(int64(i), 0), Ready: true}
		if err := reserveRestore(root, r); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := restoreEntries(root, "source")
	if err != nil || len(entries) != restoreHistoryLimit {
		t.Fatal(len(entries), err)
	}
	if entries[len(entries)-1].Created.Unix() != 2 {
		t.Fatal("did not age oldest metadata")
	}
}

func TestInstallReportsDecisionWithoutChangingState(t *testing.T) {
	// Invalid formal releases cannot touch either the OS entry or a data store.
	root := t.TempDir()
	exe := filepath.Join(root, "bin", "ownward.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0700); err != nil {
		t.Fatal(err)
	}
	p := desktop.Paths{Root: filepath.Join(root, "installed"), State: filepath.Join(root, "config", "desktop.json"), Programs: filepath.Join(root, "menu"), Desktop: filepath.Join(root, "desk")}
	var output bytes.Buffer
	err := installAt(context.Background(), []string{"--new"}, &output, io.Discard, exe, p)
	if err == nil {
		t.Fatal("accepted incomplete release")
	}
	var result entryResult
	if e := json.Unmarshal(output.Bytes(), &result); e != nil || result.Status != "failed" || result.Schema != "ownward.install/v1" {
		t.Fatal(output.String(), e)
	}
	for _, path := range []string{p.State, p.Programs, p.Desktop} {
		if _, e := os.Stat(path); !os.IsNotExist(e) {
			t.Fatal("wrote before validating release", path, e)
		}
	}
}
