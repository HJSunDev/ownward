package assetlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

const operationLimit = 4096
const operationFormat = "ownward.asset-log/v2"
const clarificationFormat = "ownward.asset-log/v3"

func (s *Store) enableOperationFormat() error {
	return s.enableFormat(operationFormat)
}

func (s *Store) enableClarifications(values []domain.Information) error {
	for _, v := range values {
		for _, r := range v.Relations {
			if r.Type == "qualifies" {
				return s.enableFormat(clarificationFormat)
			}
		}
	}
	return nil
}

func (s *Store) enableFormat(format string) error {
	path := filepath.Join(s.dir, manifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m.Format == format || m.Format == clarificationFormat {
		return nil
	}
	m.Format = format
	data, err = json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".manifest-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return replaceLogFile(f.Name(), path)
}

func (s *Store) OperationGeneration() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

func operationKey(op contract.OperationIdentity) string {
	return op.System + "/" + op.Principal + "/" + op.ID
}

func (s *Store) lookupOperation(op contract.OperationIdentity) (contract.MutationReceipt, bool, error) {
	if op.ID == "" || op.System == "" || op.Principal == "" || op.Digest == "" || op.Generation == 0 {
		return contract.MutationReceipt{}, false, errors.New("操作身份不完整")
	}
	if receipt, found := s.receipts[operationKey(op)]; found {
		if receipt.Operation != op {
			return contract.MutationReceipt{}, false, errors.New("同一操作标识不能更换内容或主体")
		}
		receipt.Results = append([]contract.MutationOutcome(nil), receipt.Results...)
		return receipt, true, nil
	}
	if op.Generation != s.generation {
		return contract.MutationReceipt{}, false, contract.ErrOperationExpired
	}
	return contract.MutationReceipt{}, false, nil
}

func (s *Store) MutationReceipt(op contract.OperationIdentity) (contract.MutationReceipt, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lookupOperation(op)
}

func (s *Store) CommitMutation(receipt contract.MutationReceipt, values []domain.Information, expected []uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found, err := s.lookupOperation(receipt.Operation); err != nil || found {
		return err
	}
	if len(s.receipts) >= operationLimit {
		if err := s.appendOperationEvent(event{Operation: "operation_generation", Generation: s.generation + 1}); err != nil {
			return err
		}
		return contract.ErrOperationExpired
	}
	entry := event{Operation: "mutation", Recorded: time.Now().UTC(), Receipt: &receipt, Values: values, Expected: expected}
	if err := s.validateMutation(entry); err != nil {
		return err
	}
	if err := s.enableClarifications(values); err != nil {
		return err
	}
	if err := s.appendOperationEvent(entry); err != nil {
		return err
	}
	return nil
}

func (s *Store) validateMutation(e event) error {
	if e.Receipt == nil || len(e.Values) != len(e.Expected) || len(e.Receipt.Results) == 0 || len(e.Receipt.Results) > 20 {
		return errors.New("变更回执无效")
	}
	seen := map[string]bool{}
	for i, value := range e.Values {
		if err := value.Validate(); err != nil {
			return err
		}
		if seen[value.ID] || s.deleted[value.ID] != 0 {
			return errors.New("重复或已遗忘的资产标识")
		}
		seen[value.ID] = true
		current, exists := s.items[value.ID]
		if e.Expected[i] == 0 {
			if exists || value.Revision != 1 {
				return errors.New("新建资产已存在或版本无效")
			}
		} else if !exists || current.Revision != e.Expected[i] || value.Revision != current.Revision+1 || value.CreatedAt != current.CreatedAt {
			return errors.New("信息版本已变化，不能覆盖")
		}
	}
	var actual []contract.AssetVersion
	for _, r := range e.Receipt.Results {
		if r.Error == "" {
			actual = append(actual, r.Asset)
		}
	}
	var want []contract.AssetVersion
	for _, v := range e.Values {
		want = append(want, contract.AssetVersion{ID: v.ID, Revision: v.Revision})
	}
	if !reflect.DeepEqual(actual, want) {
		return errors.New("回执与提交的资产不一致")
	}
	return nil
}

// An uncertain write poisons this open store. Recovery replays the complete
// record before any further writes; it never appends the operation blindly.
func (s *Store) appendOperationEvent(e event) error {
	if s.logFile == nil || s.poisoned {
		return errors.New("资产存储需要恢复核对后才能继续提交")
	}
	if err := s.enableOperationFormat(); err != nil {
		return err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	n, err := s.logFile.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = s.logFile.Sync()
	}
	if err != nil {
		s.poisoned = true
		return fmt.Errorf("提交结果尚待恢复核对: %w", err)
	}
	return s.replayOperation(e)
}

func (s *Store) replayOperation(e event) error {
	if e.Operation == "operation_generation" {
		if e.Generation <= s.generation {
			return errors.New("操作代次不递增")
		}
		s.generation = e.Generation
		s.receipts = map[string]contract.MutationReceipt{}
		return nil
	}
	if e.Receipt == nil {
		return errors.New("操作回执缺失")
	}
	op := e.Receipt.Operation
	if _, found, err := s.lookupOperation(op); err != nil || found {
		if err != nil {
			return err
		}
		return errors.New("操作日志重复提交")
	}
	if e.Operation == "mutation" {
		if err := s.validateMutation(e); err != nil {
			return err
		}
		for _, v := range e.Values {
			s.setItem(v)
		}
	}
	r := *e.Receipt
	r.Results = append([]contract.MutationOutcome(nil), r.Results...)
	s.receipts[operationKey(op)] = r
	return nil
}

func (s *Store) writeOperationSnapshot(w io.Writer) error {
	encoder := json.NewEncoder(w)
	if s.generation > 1 {
		if err := encoder.Encode(event{Operation: "operation_generation", Generation: s.generation}); err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(s.receipts))
	for key := range s.receipts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		r := s.receipts[key]
		if err := encoder.Encode(event{Operation: "operation_receipt", Receipt: &r}); err != nil {
			return err
		}
	}
	return nil
}
