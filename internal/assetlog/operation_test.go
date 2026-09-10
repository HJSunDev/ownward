package assetlog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

func TestMutationReceiptSurvivesReplayCompactionAndForget(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	v := domain.Information{Schema: domain.AssetSchema, ID: "asset", Revision: 1, Kind: domain.KindGeneral, Content: "private content to forget", CreatedAt: now, UpdatedAt: now}
	op := contract.OperationIdentity{ID: "request", System: "system", Principal: "agent", Generation: 1, Kind: "create", Digest: "input-digest"}
	r := contract.MutationReceipt{Operation: op, Results: []contract.MutationOutcome{{Asset: contract.AssetVersion{ID: v.ID, Revision: 1}}}}
	if err := s.CommitMutation(r, []domain.Information{v}, []uint64{0}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitMutation(r, []domain.Information{v}, []uint64{0}); err != nil {
		t.Fatal("same operation not replayed", err)
	}
	bad := op
	bad.Digest = "changed"
	if _, _, err := s.MutationReceipt(bad); err == nil {
		t.Fatal("different input reused receipt")
	}
	if err := s.Delete([]Deletion{{ID: v.ID, Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, found, err := s.MutationReceipt(op); err != nil || !found {
		t.Fatal("receipt lost during forgetting", found, err)
	}
	if err := s.CommitMutation(r, []domain.Information{v}, []uint64{0}); err != nil {
		t.Fatal(err)
	}
	if len(s.All()) != 0 {
		t.Fatal("replayed mutation resurrected forgotten content")
	}
	data, _ := os.ReadFile(filepath.Join(dir, logName))
	if stringsContains(data, []byte(v.Content)) {
		t.Fatal("forgotten content retained in receipt")
	}
}
func stringsContains(data, part []byte) bool {
	for i := 0; i+len(part) <= len(data); i++ {
		if string(data[i:i+len(part)]) == string(part) {
			return true
		}
	}
	return false
}

func TestMutationCrashTailAndUncertainWrite(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	op := contract.OperationIdentity{ID: "empty-batch", System: "s", Principal: "p", Generation: 1, Kind: "create_batch", Digest: "digest"}
	receipt := contract.MutationReceipt{Operation: op, Results: []contract.MutationOutcome{{Error: "invalid input"}}}
	if err := s.CommitMutation(receipt, nil, nil); err != nil {
		t.Fatal(err)
	}
	s.Close()
	f, _ := os.OpenFile(filepath.Join(dir, logName), os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(`{"operation":"mutation"`)
	f.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, found, err := s.MutationReceipt(op); !found || err != nil {
		t.Fatal("complete commit lost", err)
	}
	op.ID = "uncertain"
	receipt.Operation = op
	s.logFile.Close()
	if err := s.CommitMutation(receipt, nil, nil); err == nil || !s.poisoned {
		t.Fatal("uncertain write did not stop new commits", err)
	}
	if err := s.Compact(); err == nil {
		t.Fatal("uncertain store could be compacted")
	}
}

func TestExpiredOperationCannotBecomeANewWrite(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.mu.Lock()
	err = s.appendOperationEvent(event{Operation: "operation_generation", Generation: 2})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	op := contract.OperationIdentity{ID: "old", System: "s", Principal: "p", Generation: 1, Kind: "create", Digest: "d"}
	if _, _, err := s.MutationReceipt(op); !errors.Is(err, contract.ErrOperationExpired) {
		t.Fatal("old operation became new", err)
	}
}
