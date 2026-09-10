package hostmaterials

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/HJSunDev/ownward/internal/contract"
	"strings"
	"testing"
)

func TestWorkingSetBoundsExpiryAndReentry(t *testing.T) {
	s := WorkingSet{Version: 1}
	for i := 0; i < 10000; i++ {
		s.Observe(fmt.Sprintf("b1-ref%d", i), "read")
	}
	if len(s.Materials) != MaxReferences {
		t.Fatal(len(s.Materials))
	}
	old := s.Materials[0].Basis
	for i := 0; i < RetentionTurns; i++ {
		s.Advance(fmt.Sprint(i))
		s.Advance(fmt.Sprint(i))
		s.Check(context.Background(), func(_ context.Context, refs []string) ([]contract.InformationCheck, error) {
			results := make([]contract.InformationCheck, len(refs))
			for i, ref := range refs {
				results[i] = contract.InformationCheck{Basis: ref, Status: "unchanged"}
			}
			return results, nil
		})
	}
	if s.Turn != RetentionTurns || len(s.Materials) != MaxReferences {
		t.Fatal("重复事件推进轮次", s)
	}
	s.Advance("expiry")
	if len(s.Materials) != 0 {
		t.Fatal("自动核对使历史材料永久续期")
	}
	s.Observe(old, "unchanged")
	s.Observe(old, "read")
	if len(s.Materials) != 1 {
		t.Fatal("显式重入未去重")
	}
	s.Release([]string{old})
	if len(s.Materials) != 0 {
		t.Fatal("释放失败")
	}
	for i := 0; i < 100; i++ {
		s.Observe("b1-"+strings.Repeat("x", 2000)+fmt.Sprint(i), "read")
	}
	data, _ := json.Marshal(s)
	if len(data) > MaxBytes {
		t.Fatal("超出元数据预算")
	}
	if _, err := Decode(data); err != nil {
		t.Fatal(err)
	}
}
func TestMissingPartialAndFailedChecksNeverBecomeVerified(t *testing.T) {
	s := WorkingSet{Version: 1}
	s.Observe("b1-first", "read")
	s.Observe("b1-second", "read")
	result := s.Check(context.Background(), func(_ context.Context, refs []string) ([]contract.InformationCheck, error) {
		return []contract.InformationCheck{{Basis: refs[0], Status: "unchanged"}}, nil
	})
	if result[0].Status != "unchanged" || result[1].Status != "unverifiable" {
		t.Fatal(result)
	}
	result = s.Check(context.Background(), func(_ context.Context, refs []string) ([]contract.InformationCheck, error) {
		return nil, context.Canceled
	})
	if result[0].Status != "unverifiable" || result[1].Status != "unverifiable" {
		t.Fatal(result)
	}
	if _, err := Decode([]byte(`{"version":1,"materials":[{"basis":"wrong"}]}`)); err == nil {
		t.Fatal("伪造工作集被接受")
	}
}
