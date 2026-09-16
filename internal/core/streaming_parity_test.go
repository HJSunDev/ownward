package core

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func TestStreamingCombinedSearchMatchesExisting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	assets, e := assetlog.Open(filepath.Join(root, "old"))
	if e != nil {
		t.Fatal(e)
	}
	state, e := derived.Open(filepath.Join(root, "derived"))
	if e != nil {
		t.Fatal(e)
	}
	old, e := newTestCollaborative(t, assets, state, embedding.HashForTesting{Dimensions: 512})
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	var saved []domain.Information
	for _, text := range []string{"项目负责人是李明。预算是三万元。", strings.Repeat("无关背景材料。", 75) + "\n\n负责项目交付的李明将于周五提交方案。\n\n" + strings.Repeat("参考内容。", 80), "预算三万元属于研发部门。", "英文 reference 文档包含 alpha 和 beta。"} {
		input := CreateInput{Content: text, Contexts: []domain.Context{{Key: "Scope", Value: "Example"}}}
		if len(saved) == 3 {
			input.Relations = []domain.ExplicitRelation{{Type: "related_to", TargetID: saved[0].ID}}
		}
		r, e := old.Create(ctx, input)
		if e != nil {
			t.Fatal(e)
		}
		saved = append(saved, r.Information)
	}
	for i, asset := range saved {
		org := &semantics.Organization{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{{ID: "whole"}}}
		var candidates []semantics.Candidate
		if i == 3 {
			org.Links = []semantics.GroundedLink{{Type: "supports", Meaning: "相关预算的原始依据。", Source: semantics.GraphEndpoint{AssetID: asset.ID, UnitID: "whole"}, Target: semantics.GraphEndpoint{AssetID: saved[0].ID, Revision: saved[0].Revision}}}
			candidates = []semantics.Candidate{{ID: saved[0].ID, Revision: saved[0].Revision, Content: saved[0].Content}}
		}
		normalized, e := semantics.NormalizeOrganization(asset, org, candidates)
		if e != nil {
			t.Fatal(e)
		}
		r, ok := state.GetWithEmbedding(asset.ID)
		if !ok {
			t.Fatal("missing record")
		}
		r.Analysis = semantics.Analysis{Summary: truncate(asset.Content, 80), Organization: normalized, Relations: r.Analysis.Relations}
		r.Status = "ready"
		r.SemanticWorkReference = nil
		r.InputAssets = nil
		r.InputsKnown = true
		if len(r.Embedding) == 0 {
			vectors, e := old.embedder.EmbedDocuments(ctx, []string{asset.Content})
			if e != nil {
				t.Fatal(e)
			}
			r.Embedding = vectors[0]
		}
		for _, rel := range asset.Relations {
			r.Analysis.Relations = append(r.Analysis.Relations, semantics.Relation{Type: rel.Type, TargetID: rel.TargetID, Confidence: 1})
		}
		if e = state.Put(r); e != nil {
			t.Fatal(e)
		}
		old.semantic.Upsert(r)
	}
	budget, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
	db, e := boundedstore.Open(ctx, filepath.Join(root, "new.sqlite"), boundedstore.Options{Budget: budget})
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	generation, e := db.InitializeGeneration(ctx, old.embedder.Space().ID)
	if e != nil {
		t.Fatal(e)
	}
	s := &StreamingAssets{Store: db, Budget: budget, Scratch: root, DiskBytes: 128 * resourcebudget.MiB, Embedder: old.embedder}
	for _, a := range saved {
		op := contract.OperationIdentity{ID: a.ID, System: "parity", Principal: "test", Generation: 1, Kind: "ownward_create", Digest: a.ID}
		key, e := boundedstore.OperationKey(op)
		if e != nil {
			t.Fatal(e)
		}
		data, _ := json.Marshal(a)
		payload, e := db.Stage(ctx, key, boundedstore.StringSource(a.Content), boundedstore.StringSource(data))
		if e != nil {
			t.Fatal(e)
		}
		write := boundedstore.AssetWrite{Meta: contract.AssetMeta{ID: a.ID, Revision: a.Revision, Kind: a.Kind, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}, Payload: payload}
		if e = db.Publish(ctx, contract.MutationReceipt{Operation: op, Results: []contract.MutationOutcome{{Asset: contract.AssetVersion{ID: a.ID, Revision: 1}}}}, []boundedstore.AssetWrite{write}); e != nil {
			t.Fatal(e)
		}
	}
	for _, a := range saved {
		record, ok := state.GetWithEmbedding(a.ID)
		if !ok {
			t.Fatal("missing record")
		}
		data, _ := json.Marshal(record)
		v, e := db.StageOrganization(ctx, generation, boundedstore.StringSource(data))
		if e != nil {
			t.Fatal(e)
		}
		if e = db.PublishOrganization(ctx, v); e != nil {
			t.Fatal(e)
		}
	}
	for _, query := range []string{"项目 李明", "预算 研发", "alpha beta", "not present", saved[0].ID, "交付 周五", saved[0].ID + strings.Repeat(" ", 260) + "预算"} {
		for _, limit := range []int{1, 3, 10} {
			want, e := old.Search(ctx, SearchInput{Query: query, Limit: limit})
			if e != nil {
				t.Fatal(e)
			}
			data, _ := json.Marshal(map[string]any{"query": query, "limit": limit})
			doc, e := streamjson.Parse(ctx, root, strings.NewReader(string(data)), budget, 128*resourcebudget.MiB)
			if e != nil {
				t.Fatal(e)
			}
			out, e := s.searchTool(ctx, doc.Root())
			doc.Close()
			if e != nil {
				t.Fatalf("query %q: %v", query, e)
			}
			r, e := out.Value.Open(ctx)
			if e != nil {
				t.Fatal(e)
			}
			raw, e := io.ReadAll(r)
			r.Close()
			out.Close()
			if e != nil {
				t.Fatal(e)
			}
			var got struct {
				Results []SearchResult `json:"results"`
			}
			if e = json.Unmarshal(raw, &got); e != nil {
				t.Fatal(e)
			}
			left, _ := json.Marshal(got.Results)
			right, _ := json.Marshal(want)
			if !reflect.DeepEqual(left, right) {
				t.Fatalf("query %q limit %d\ngot %s\nwant%s", query, limit, left, right)
			}
		}
	}
	for _, start := range saved {
		want, e := old.Navigate(ctx, []string{start.ID}, nil, 2, 20)
		if e != nil {
			t.Fatal(e)
		}
		data, _ := json.Marshal(map[string]any{"start_ids": []string{start.ID}, "depth": 2, "limit": 20})
		doc, e := streamjson.Parse(ctx, root, strings.NewReader(string(data)), budget, 128*resourcebudget.MiB)
		if e != nil {
			t.Fatal(e)
		}
		out, e := s.navigateTool(ctx, doc.Root())
		doc.Close()
		if e != nil {
			t.Fatal(e)
		}
		r, e := out.Value.Open(ctx)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(r)
		r.Close()
		out.Close()
		if e != nil {
			t.Fatal(e)
		}
		var wrapped struct {
			Result NavigationResult `json:"result"`
		}
		if e = json.Unmarshal(raw, &wrapped); e != nil {
			t.Fatal(e)
		}
		a, _ := json.Marshal(wrapped.Result)
		b, _ := json.Marshal(want)
		if string(a) != string(b) {
			t.Fatalf("navigation got %s want %s", a, b)
		}
	}

}
