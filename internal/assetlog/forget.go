package assetlog

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Deletion 只保留删除所需身份，不保存被遗忘内容。
type Deletion struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

func (s *Store) Delete(values []Deletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logFile == nil || s.poisoned {
		return errors.New("信息资产日志已关闭")
	}
	entry := event{Operation: "forget", Recorded: time.Now().UTC(), Deleted: values}
	if err := s.validateDeletion(entry); err != nil {
		return err
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	start, err := s.logFile.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	n, err := s.logFile.Write(encoded)
	if err == nil && n != len(encoded) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = s.logFile.Sync()
	}
	if err != nil {
		return errors.Join(err, s.rollbackLocked(start))
	}
	return s.replayDeletion(entry)
}

func (s *Store) validateDeletion(entry event) error {
	if len(entry.Deleted) == 0 {
		return errors.New("遗忘目标不能为空")
	}
	seen := map[string]bool{}
	for _, v := range entry.Deleted {
		if strings.TrimSpace(v.ID) == "" || v.Revision == 0 || seen[v.ID] {
			return errors.New("遗忘目标无效或重复")
		}
		seen[v.ID] = true
		if previous := s.deleted[v.ID]; previous != 0 {
			if previous != v.Revision {
				return errors.New("已遗忘资产版本不一致")
			}
			continue
		}
		current, exists := s.items[v.ID]
		if entry.Operation == "forget" && (!exists || current.Revision != v.Revision) {
			return errors.New("遗忘目标已变化，需重新核对")
		}
		if entry.Operation == "forgotten" && exists {
			return errors.New("删除快照与有效资产冲突")
		}
	}
	return nil
}

func (s *Store) replayDeletion(entry event) error {
	if err := s.validateDeletion(entry); err != nil {
		return err
	}
	for _, v := range entry.Deleted {
		s.removeSource(v.ID)
		delete(s.items, v.ID)
		s.deleted[v.ID] = v.Revision
	}
	return nil
}

func (s *Store) writeDeletedSnapshot(w io.Writer) error {
	if err := s.writeOperationSnapshot(w); err != nil {
		return err
	}
	if len(s.deleted) == 0 {
		return nil
	}
	values := make([]Deletion, 0, len(s.deleted))
	for id, revision := range s.deleted {
		values = append(values, Deletion{id, revision})
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return json.NewEncoder(w).Encode(event{Operation: "forgotten", Deleted: values})
}

func (s *Store) writeLiveSnapshot(w io.Writer) error {
	if err := s.writeDeletedSnapshot(w); err != nil {
		return err
	}
	ids := make([]string, 0, len(s.items))
	for id := range s.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	encoder := json.NewEncoder(w)
	for _, id := range ids {
		value := s.items[id]
		if err := encoder.Encode(event{Operation: "snapshot", Recorded: value.UpdatedAt.UTC(), Value: value}); err != nil {
			return err
		}
	}
	return nil
}

// PurgeTemporary 清除崩溃可能留下的本存储临时副本；不触碰独立用户备份。
func (s *Store) PurgeTemporary() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".information-compacting-") {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
