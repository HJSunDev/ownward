package core

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func TestStreamingCombinedSearchAndNavigation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	budget, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
	db, e := boundedstore.Open(ctx, filepath.Join(root, "new.sqlite"), boundedstore.Options{Budget: budget})
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	embedder := embedding.HashForTesting{Dimensions: 512}
	generation, e := db.InitializeGeneration(ctx, embedder.Space().ID)
	if e != nil {
		t.Fatal(e)
	}
	s := &StreamingAssets{Store: db, Budget: budget, Scratch: root, DiskBytes: 128 * resourcebudget.MiB, Embedder: embedder}

	texts := []string{
		"项目负责人是李明。预算是三万元。",
		strings.Repeat("无关背景材料。", 75) + "\n\n负责项目交付的李明将于周五提交方案。\n\n" + strings.Repeat("参考内容。", 80),
		"预算三万元属于研发部门。",
		"英文 reference 文档包含 alpha 和 beta。",
	}
	now := time.Now().UTC().Truncate(time.Second)
	var saved []domain.Information
	for i, text := range texts {
		id, err := newID(now.Add(time.Duration(i) * time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		a := domain.Information{
			Schema: domain.AssetSchema, ID: id, Revision: 1,
			CreatedAt: now, UpdatedAt: now, Kind: domain.KindGeneral,
			Content:  text,
			Contexts: []domain.Context{{Key: "Scope", Value: "Example"}},
		}
		if i == 3 {
			a.Relations = []domain.ExplicitRelation{{Type: "related_to", TargetID: saved[0].ID}}
		}
		saved = append(saved, a)
		op := contract.OperationIdentity{ID: a.ID, System: "parity", Principal: "test", Generation: 1, Kind: "ownward_create", Digest: a.ID}
		key, err := boundedstore.OperationKey(op)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(a)
		payload, err := db.Stage(ctx, key, boundedstore.StringSource(a.Content), boundedstore.StringSource(data))
		if err != nil {
			t.Fatal(err)
		}
		write := boundedstore.AssetWrite{Meta: contract.AssetMeta{ID: a.ID, Revision: a.Revision, Kind: a.Kind, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}, Payload: payload}
		if err := db.Publish(ctx, contract.MutationReceipt{Operation: op, Results: []contract.MutationOutcome{{Asset: contract.AssetVersion{ID: a.ID, Revision: 1}}}}, []boundedstore.AssetWrite{write}); err != nil {
			t.Fatal(err)
		}
	}

	for i, asset := range saved {
		org := &semantics.Organization{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{{ID: "whole"}}}
		var candidates []semantics.Candidate
		if i == 3 {
			org.Links = []semantics.GroundedLink{{Type: "supports", Meaning: "相关预算的原始依据。", Source: semantics.GraphEndpoint{AssetID: asset.ID, UnitID: "whole"}, Target: semantics.GraphEndpoint{AssetID: saved[0].ID, Revision: saved[0].Revision}}}
			candidates = []semantics.Candidate{{ID: saved[0].ID, Revision: saved[0].Revision, Content: saved[0].Content}}
		}
		normalized, err := semantics.NormalizeOrganization(asset, org, candidates)
		if err != nil {
			t.Fatal(err)
		}
		vectors, err := embedder.EmbedDocuments(ctx, []string{asset.Content})
		if err != nil {
			t.Fatal(err)
		}
		record := derived.Record{
			AssetID: asset.ID, AssetRevision: asset.Revision, GeneratedAt: now,
			Provider: embedder.Name(), Status: "ready", InputsKnown: true,
			EmbeddingSpace: embedder.Space().ID, Embedding: vectors[0],
			Analysis: semantics.Analysis{Summary: truncate(asset.Content, 80), Organization: normalized},
		}
		for _, rel := range asset.Relations {
			record.Analysis.Relations = append(record.Analysis.Relations, semantics.Relation{Type: rel.Type, TargetID: rel.TargetID, Confidence: 1})
		}
		data, _ := json.Marshal(record)
		v, err := db.StageOrganization(ctx, generation, boundedstore.StringSource(data))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.PublishOrganization(ctx, v); err != nil {
			t.Fatal(err)
		}
	}

	runSearch := func(query string, limit int) []SearchResult {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"query": query, "limit": limit})
		doc, err := streamjson.Parse(ctx, root, strings.NewReader(string(data)), budget, 128*resourcebudget.MiB)
		if err != nil {
			t.Fatal(err)
		}
		out, err := s.searchTool(ctx, doc.Root())
		doc.Close()
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		r, err := out.Value.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(r)
		r.Close()
		out.Close()
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Results []SearchResult `json:"results"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		return got.Results
	}

	idHits := runSearch(saved[0].ID, 3)
	if len(idHits) == 0 || idHits[0].ID != saved[0].ID || !contains(idHits[0].Signals, "identity") {
		t.Fatalf("expected identity match on saved[0], got %#v", idHits)
	}
	alphaHits := runSearch("alpha beta", 10)
	if len(alphaHits) < 2 || alphaHits[0].ID != saved[3].ID {
		t.Fatalf("expected lexical+relation results for alpha beta, got %#v", alphaHits)
	}
	foundRelated := false
	for _, item := range alphaHits {
		if item.ID == saved[0].ID && contains(item.Signals, "relation") {
			foundRelated = true
		}
	}
	if !foundRelated {
		t.Fatalf("expected grounded relation entrance to saved[0], got %#v", alphaHits)
	}
	longHits := runSearch("交付 周五", 3)
	if len(longHits) == 0 || longHits[0].ID != saved[1].ID || len(longHits[0].Evidence) == 0 {
		t.Fatalf("expected long-asset evidence selection on saved[1], got %#v", longHits)
	}

	data, _ := json.Marshal(map[string]any{"start_ids": []string{saved[3].ID}, "depth": 2, "limit": 20})
	doc, err := streamjson.Parse(ctx, root, strings.NewReader(string(data)), budget, 128*resourcebudget.MiB)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.navigateTool(ctx, doc.Root())
	doc.Close()
	if err != nil {
		t.Fatal(err)
	}
	r, err := out.Value.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(r)
	r.Close()
	out.Close()
	if err != nil {
		t.Fatal(err)
	}
	var wrapped struct {
		Result NavigationResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		t.Fatal(err)
	}
	if len(wrapped.Result.Edges) < 2 || len(wrapped.Result.Nodes) < 2 {
		t.Fatalf("expected explicit + grounded navigation edges between saved[3] and saved[0], got %#v", wrapped.Result)
	}
}
