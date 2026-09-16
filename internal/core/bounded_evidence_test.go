package core

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func TestBoundedEvidencePreservesExistingSelection(t *testing.T) {
	for _, text := range []string{
		strings.Repeat("无关内容中文🙂 alpha ", 130) + "\n\n目标事实 beta evidence。\n\n" + strings.Repeat("tail text ", 170),
		strings.Repeat("word ", 300) + "\r\n\r\n" + strings.Repeat("beta 目标关系。", 250),
		strings.Repeat("长", 1700) + "目标。",
	} {
		ctx := context.Background()
		b, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
		s, e := boundedstore.Open(ctx, filepath.Join(t.TempDir(), "test.sqlite"), boundedstore.Options{Budget: b})
		if e != nil {
			t.Fatal(e)
		}
		op := contract.OperationIdentity{ID: "evidence", System: "s", Principal: "p", Generation: 1, Kind: "ownward_create", Digest: "evidence"}
		key, e := boundedstore.OperationKey(op)
		if e != nil {
			t.Fatal(e)
		}
		p, e := s.Stage(ctx, key, boundedstore.StringSource(text), nil)
		if e != nil {
			t.Fatal(e)
		}
		v := boundedstore.AssetWrite{Meta: contract.AssetMeta{ID: "a", Revision: 1, Kind: domain.KindGeneral, CreatedAt: time.Now(), UpdatedAt: time.Now()}, Payload: p}
		e = s.Publish(ctx, contract.MutationReceipt{Operation: op, Results: []contract.MutationOutcome{{Asset: contract.AssetVersion{ID: "a", Revision: 1}}}}, []boundedstore.AssetWrite{v})
		if e != nil {
			t.Fatal(e)
		}
		for _, query := range []string{"目标 beta", "alpha word", "不存在", "长"} {
			for _, limit := range []int{1, 3, 8} {
				got, e := s.RankEvidence(ctx, "", "a", query, limit)
				if e != nil {
					t.Fatal(e)
				}
				want := rankEvidence(domain.Information{ID: "a", Revision: 1, Content: text}, query, limit)
				if len(got) != len(want) || len(got) > 0 && !reflect.DeepEqual(got, want) {
					t.Fatalf("query%q limit%d\ngot %+v\nwant %+v", query, limit, got, want)
				}
			}
		}
		s.Close()
	}
}
