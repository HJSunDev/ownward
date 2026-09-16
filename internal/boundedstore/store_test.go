package boundedstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func openTest(t *testing.T, path string) *Store {
	t.Helper()
	budget, _ := resourcebudget.New(8*resourcebudget.MiB, resourcebudget.MiB)
	s, err := Open(context.Background(), path, Options{Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}
func stageTest(t *testing.T, s *Store, op contract.OperationIdentity, id, text string, revision uint64) AssetWrite {
	t.Helper()
	p, err := s.Stage(context.Background(), operationKey(op), StringSource(text), nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	return AssetWrite{Meta: contract.AssetMeta{ID: id, Revision: revision, CreatedAt: now, UpdatedAt: now, Kind: domain.KindGeneral}, Payload: p, ExpectedRevision: revision - 1}
}
func operation(id string) contract.OperationIdentity {
	return contract.OperationIdentity{ID: id, System: "system", Principal: "principal", Generation: 1, Kind: "ownward_create", Digest: id}
}
func receipt(op contract.OperationIdentity, v ...AssetWrite) contract.MutationReceipt {
	r := contract.MutationReceipt{Operation: op}
	for _, a := range v {
		r.Results = append(r.Results, contract.MutationOutcome{Asset: contract.AssetVersion{ID: a.Meta.ID, Revision: a.Meta.Revision}})
	}
	return r
}
func readTest(t *testing.T, s *Store, id string) string {
	t.Helper()
	r, err := s.OpenContent(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAtomicVisibilityRetryAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ownward.sqlite")
	s := openTest(t, path)
	text := " \n" + strings.Repeat("原文🙂\"\\", 14000) + "\r\n "
	op := operation("first")
	v := stageTest(t, s, op, "asset", text, 1)
	if _, err := s.ReadAssetMeta(ctx, "asset", 0); !errors.Is(err, ErrNotFound) {
		t.Fatal("staging visible", err)
	}
	if err := s.Publish(ctx, receipt(op, v), []AssetWrite{v}); err != nil {
		t.Fatal(err)
	}
	if err := s.Publish(ctx, receipt(op, v), []AssetWrite{v}); err != nil {
		t.Fatal("idempotent replay", err)
	}
	if got := readTest(t, s, "asset"); got != text {
		t.Fatal("original changed")
	}
	m, err := s.ReadAssetMeta(ctx, "asset", 1)
	if err != nil || m.ContentBytes != int64(len(text)) {
		t.Fatal(m, err)
	}
	op.Digest = "changed"
	if _, _, err := s.MutationReceipt(ctx, op); err == nil {
		t.Fatal("accepted changed replay")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, path)
	if readTest(t, s, "asset") != text {
		t.Fatal("restart lost content")
	}
}

func TestBatchRollbackAndOptimisticRevision(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "ownward.sqlite"))
	ctx := context.Background()
	op := operation("first")
	v := stageTest(t, s, op, "a", "original", 1)
	if err := s.Publish(ctx, receipt(op, v), []AssetWrite{v}); err != nil {
		t.Fatal(err)
	}
	op2 := operation("update")
	a := stageTest(t, s, op2, "a", "changed", 2)
	b := stageTest(t, s, op2, "b", "bad version", 2)
	if err := s.Publish(ctx, receipt(op2, a, b), []AssetWrite{a, b}); err == nil {
		t.Fatal("invalid batch committed")
	}
	if readTest(t, s, "a") != "original" {
		t.Fatal("half batch published")
	}
	if _, found, err := s.MutationReceipt(ctx, op2); err != nil || found {
		t.Fatal("receipt survived rollback", found, err)
	}
	if err := s.Publish(ctx, receipt(op2, a), []AssetWrite{a}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadAssetMeta(ctx, "a", 1); !errors.Is(err, ErrNotFound) {
		t.Fatal("old revision delivered", err)
	}
}

func TestCommitGuardAndRanges(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "ownward.sqlite"))
	ctx := context.Background()
	op := operation("guard")
	text := strings.Repeat("x", ChunkBytes-1) + "中文资料" + strings.Repeat("y", ChunkBytes)
	v := stageTest(t, s, op, "a", text, 1)
	denied := errors.New("revoked")
	bound := contract.WithCommitGuard(ctx, func(func() error) error { return denied })
	if err := s.Publish(bound, receipt(op, v), []AssetWrite{v}); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if _, err := s.ReadAssetMeta(ctx, "a", 0); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Publish(ctx, receipt(op, v), []AssetWrite{v}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.ReadRanges(ctx, "a", 1, []contract.ContentRange{{Offset: ChunkBytes - 1, Length: int64(len("中文资料"))}}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "中文资料" {
		t.Fatal(out.String())
	}
}

func TestInterruptedStageIsInvisible(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "ownward.sqlite"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Stage(ctx, "op", StringSource("text"), nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.Stage(context.Background(), "op", StringSource(" \t\r\n"), nil); err == nil {
		t.Fatal("blank content accepted")
	}
	page, err := s.ScanAssets(context.Background(), "", 1024)
	if err != nil || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
}
