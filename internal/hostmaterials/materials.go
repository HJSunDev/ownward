// Package hostmaterials 管理宿主近期材料引用，不保存原文或执行语义判断。
package hostmaterials

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/HJSunDev/ownward/internal/contract"
)

const MaxReferences = 32
const MaxBytes = 64 << 10
const RetentionTurns = 8

type Material struct {
	Basis    string `json:"basis"`
	UsedTurn uint64 `json:"used_turn"`
	Status   string `json:"status"`
}
type WorkingSet struct {
	Version   int        `json:"version"`
	Turn      uint64     `json:"turn"`
	LastTurn  string     `json:"last_turn,omitempty"`
	Materials []Material `json:"materials"`
}
type Checker func(context.Context, []string) ([]contract.InformationCheck, error)

func (s *WorkingSet) valid() bool { return s.Version == 1 && len(s.Materials) <= MaxReferences }
func Decode(data []byte) (WorkingSet, error) {
	var s WorkingSet
	if len(data) > MaxBytes || json.Unmarshal(data, &s) != nil || !s.valid() {
		return WorkingSet{}, errors.New("材料工作集不可恢复")
	}
	for _, m := range s.Materials {
		if !validBasis(m.Basis) || m.UsedTurn > s.Turn {
			return WorkingSet{}, errors.New("材料引用不可恢复")
		}
	}
	return s, nil
}
func validBasis(ref string) bool {
	if len(ref) > 2048 || !strings.HasPrefix(ref, "b1-") {
		return false
	}
	for _, r := range ref[3:] {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return len(ref) > 3
}
func (s *WorkingSet) Observe(ref, status string) {
	if !validBasis(ref) {
		return
	}
	s.Version = 1
	s.Materials = slices.DeleteFunc(s.Materials, func(m Material) bool { return m.Basis == ref })
	s.Materials = append(s.Materials, Material{Basis: ref, UsedTurn: s.Turn, Status: status})
	s.trim()
}
func (s *WorkingSet) Release(refs []string) {
	s.Materials = slices.DeleteFunc(s.Materials, func(m Material) bool { return slices.Contains(refs, m.Basis) })
}
func (s *WorkingSet) Advance(turn string) {
	s.Version = 1
	if turn != "" && s.LastTurn == turn {
		return
	}
	s.LastTurn = turn
	s.Turn++
	s.Materials = slices.DeleteFunc(s.Materials, func(m Material) bool { return s.Turn-m.UsedTurn > RetentionTurns })
	s.trim()
}
func (s *WorkingSet) trim() {
	for len(s.Materials) > 0 {
		data, _ := json.Marshal(s)
		if len(s.Materials) <= MaxReferences && len(data) <= MaxBytes {
			return
		}
		s.Materials = s.Materials[1:]
	}
}
func (s *WorkingSet) Check(ctx context.Context, check Checker) []contract.InformationCheck {
	refs := make([]string, len(s.Materials))
	results := make([]contract.InformationCheck, len(refs))
	for i := range refs {
		refs[i] = s.Materials[i].Basis
		results[i] = contract.InformationCheck{Basis: refs[i], Status: "unverifiable"}
		s.Materials[i].Status = "unverifiable"
	}
	if len(refs) == 0 {
		return results
	}
	got, err := check(ctx, refs)
	if err == nil {
		for i, r := range got {
			if i >= len(refs) {
				break
			}
			if r.Basis != refs[i] {
				continue
			}
			switch r.Status {
			case "unchanged", "changed", "unavailable", "unverifiable":
				results[i] = r
				s.Materials[i].Status = r.Status
			}
		}
	}
	// 自动核对不续期；已不能使用的引用退出自动工作集。
	s.Materials = slices.DeleteFunc(s.Materials, func(m Material) bool { return m.Status == "unavailable" })
	return results
}
