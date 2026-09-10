package derived

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/semantics"
)

// Inputs 包含参与判断的原文和此前分析的来源，关系边不能替代实际输入依赖。
func Inputs(record Record) []semantics.CandidateReference {
	values := append([]semantics.CandidateReference(nil), record.InputAssets...)
	if record.SemanticWorkReference != nil {
		values = append(values, record.SemanticWorkReference.Candidates...)
	}
	for _, relation := range record.Analysis.Relations {
		values = append(values, semantics.CandidateReference{ID: relation.TargetID, Revision: relation.TargetRevision})
		if relation.InferredBy != "" {
			values = append(values, semantics.CandidateReference{ID: relation.InferredBy, Revision: relation.TargetRevision})
		}
	}
	seen := map[string]bool{}
	result := make([]semantics.CandidateReference, 0, len(values))
	for _, v := range values {
		if v.ID != "" && v.ID != record.AssetID && !seen[v.ID] {
			result = append(result, v)
			seen[v.ID] = true
		}
	}
	return result
}

// PurgeInactive 删除受本存储控制的旧世代，不能留下可重新挂载的原文派生副本。
func (s *Store) PurgeInactive() error {
	s.mu.RLock()
	root, active := s.root, s.directory
	s.mu.RUnlock()
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(root, generationsName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if !validGenerationID(entry.Name()) {
			continue
		}
		path := filepath.Join(root, generationsName, entry.Name())
		if path == active {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("派生世代包含非预期符号链接")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return errors.New("派生清理路径超出存储范围")
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	if active != root {
		for _, name := range []string{LogFileName, legacyLogFileName} {
			if err := os.Remove(filepath.Join(root, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	for _, directory := range []string{root, active} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if strings.HasPrefix(entry.Name(), ".organization-compacting-") || strings.HasPrefix(entry.Name(), ".organization-migrating-") {
				if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
