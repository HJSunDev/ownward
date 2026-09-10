package assetlog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

// 所有派生元数据与 items 共用锁和提交点，回放时重建。
func (s *Store) setItem(value domain.Information) {
	if s.fingerprints == nil {
		s.fingerprints = map[string]string{}
		s.qualifiers = map[string]map[string]bool{}
	}
	s.removeSource(value.ID)
	s.items[value.ID] = clone(value)
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	s.fingerprints[value.ID] = hex.EncodeToString(sum[:])
	for _, r := range value.Relations {
		if r.Type == "qualifies" {
			if s.qualifiers[r.TargetID] == nil {
				s.qualifiers[r.TargetID] = map[string]bool{}
			}
			s.qualifiers[r.TargetID][value.ID] = true
		}
	}
}
func (s *Store) removeSource(id string) {
	for _, r := range s.items[id].Relations {
		if r.Type == "qualifies" {
			delete(s.qualifiers[r.TargetID], id)
			if len(s.qualifiers[r.TargetID]) == 0 {
				delete(s.qualifiers, r.TargetID)
			}
		}
	}
	delete(s.fingerprints, id)
}
func (s *Store) ReadSource(id string) (contract.SourceSnapshot, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.logFile == nil || s.poisoned {
		return contract.SourceSnapshot{}, false, errors.New("资产权威不可读取")
	}
	v, ok := s.items[id]
	if !ok {
		return contract.SourceSnapshot{}, false, nil
	}
	result := contract.SourceSnapshot{Information: clone(v), Fingerprint: s.fingerprints[id]}
	ids := make([]string, 0, len(s.qualifiers[id]))
	for source := range s.qualifiers[id] {
		ids = append(ids, source)
	}
	sort.Strings(ids)
	for _, source := range ids {
		note := clone(s.items[source])
		for _, r := range note.Relations {
			if r.Type == "qualifies" && r.TargetID == id {
				result.Clarifications = append(result.Clarifications, contract.ClarificationSource{Information: note, Fingerprint: s.fingerprints[source], Selector: r.Selector})
			}
		}
	}
	return result, true, nil
}
