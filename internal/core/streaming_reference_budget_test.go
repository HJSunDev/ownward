package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func TestStreamingReferenceIdentityBounds(t *testing.T) {
	budget, _ := resourcebudget.New(4*resourcebudget.MiB, 0)
	ctx := resourcebudget.WithContext(context.Background(), budget)
	dir := t.TempDir()
	s := &StreamingAssets{Budget: budget, Scratch: dir, DiskBytes: 32 * resourcebudget.MiB}
	for _, size := range []int{60 * 1024, 257, 256} {
		refs := make([]semantics.CandidateReference, 660)
		for i := range refs {
			refs[i] = semantics.CandidateReference{ID: fmt.Sprintf("%03d", i) + strings.Repeat("x", size-3), Revision: 1, OrganizationSnapshot: "org_" + strings.Repeat("a", 64)}
		}
		data, _ := json.Marshal(map[string]any{"input_assets": refs, "analysis": map[string]any{}})
		doc, err := streamjson.Parse(ctx, dir, strings.NewReader(string(data)), budget, 64*resourcebudget.MiB)
		if err != nil {
			t.Fatal(err)
		}
		sub, _, err := s.readSemanticHeader(ctx, doc.Root(), domain.Information{})
		doc.Close()
		if size > 256 {
			if err == nil || len(sub.InputAssets) != 0 {
				t.Fatalf("invalid identity retained: size=%d count=%d err=%v", size, len(sub.InputAssets), err)
			}
		} else if err != nil || len(sub.InputAssets) != len(refs) {
			t.Fatalf("complete valid batch lost: count=%d err=%v", len(sub.InputAssets), err)
		}
	}
}
