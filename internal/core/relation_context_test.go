package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestRelationLineContextPreservesOriginalSpans(t *testing.T) {
	content := "头部\r\n仅在 Windows 上用 🐕 工具，限临时备份。\r\nalpha βeta\n结尾"
	asset := domain.Information{ID: "a", Revision: 1, Content: content}
	runes := []rune(content)
	for start := 0; start < len(runes); start++ {
		for end := start + 1; end <= len(runes); end++ {
			original, err := derived.MaterializeEvidenceUnit(asset, derived.EvidenceUnit{Schema: derived.EvidenceUnitSchema, SourceID: "a", SourceRevision: 1, StartRune: start, EndRune: end, StartByte: len(string(runes[:start])), EndByte: len(string(runes[:end]))})
			if err != nil {
				t.Fatal(err)
			}
			refs, err := relationLineContext(asset, original.Reference())
			if err != nil {
				t.Fatalf("%d:%d %v", start, end, err)
			}
			seen := map[string]bool{}
			if len(refs) > 2 {
				t.Fatal("more than two boundary lines")
			}
			for _, ref := range refs {
				if seen[ref.ID] {
					t.Fatal("duplicate line context")
				}
				seen[ref.ID] = true
				if ref.SourceID != "a" || ref.SourceRevision != 1 {
					t.Fatal("changed provenance")
				}
				span, err := derived.ParseEvidenceUnitID(ref.ID)
				if err != nil {
					t.Fatal(err)
				}
				_, err = derived.MaterializeEvidenceUnit(asset, span)
				if err != nil {
					t.Fatal(err)
				}
				if content[span.StartByte:span.EndByte] != string(runes[ref.StartRune:ref.EndRune]) {
					t.Fatal("changed original bytes")
				}
				if ref.StartRune > 0 && runes[ref.StartRune-1] != '\n' {
					t.Fatal("partial start")
				}
				if ref.EndRune < len(runes) && runes[ref.EndRune-1] != '\n' {
					t.Fatal("partial end")
				}
			}
		}
	}
	full, err := sourceReference(asset, domain.TextSelector{})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := relationLineContext(asset, full)
	if err != nil || len(refs) != 0 {
		t.Fatal("full source duplicated", err)
	}
	asset.Revision++
	if _, err = relationLineContext(asset, full); err == nil {
		t.Fatal("stale context accepted")
	}
}

func TestRelationNavigationReturnsCutLineConditionsWithoutChangingClaim(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := derived.Open(filepath.Join(root, "derived"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := newTestCollaborative(t, assets, store, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	line := "用 CopyTool 进行备份；" + strings.Repeat("这是操作说明。", 65) + "仅适用于 Windows，而且只作为临时方案。"
	content := "这是一份备份计划。\n" + line + "\n其他不相关内容。"
	created, err := service.Create(ctx, CreateInput{Content: content})
	if err != nil {
		t.Fatal(err)
	}
	works, err := service.SemanticWorkFor(ctx, []string{created.Information.ID})
	if err != nil || len(works) != 1 {
		t.Fatal(err)
	}
	meaning := "这份计划采用 CopyTool 备份。"
	org := &semantics.Organization{Schema: semantics.OrganizationSchema, Links: []semantics.GroundedLink{{Type: "related_to", Meaning: meaning,
		Source: semantics.GraphEndpoint{AssetID: created.Information.ID, Selector: domain.TextSelector{Exact: "CopyTool"}},
		Target: semantics.GraphEndpoint{AssetID: created.Information.ID, Selector: domain.TextSelector{Exact: "备份计划"}},
	}}}
	if _, err = service.SubmitSemantic(ctx, semanticSubmission(works[0], semantics.Analysis{Organization: org})); err != nil {
		t.Fatal(err)
	}
	nav, err := service.Navigate(ctx, []string{created.Information.ID}, []string{"related_to"}, 1, 5)
	if err != nil || len(nav.Edges) != 1 {
		t.Fatalf("navigation %v %#v", err, nav)
	}
	relation := nav.Edges[0].Grounded
	if relation.Type != "related_to" || relation.Meaning != meaning || len(relation.Conditions) != 0 {
		t.Fatal("changed semantic judgement")
	}
	for i, ref := range []domain.EvidenceReference{relation.Source, relation.Target} {
		evidence, e := service.ReadEvidence(ctx, ref.ID)
		if e != nil {
			t.Fatal(e)
		}
		if evidence.Content != []string{"CopyTool", "备份计划"}[i] {
			t.Fatal("anchor changed")
		}
	}
	seen := map[string]bool{}
	found := false
	for _, ref := range relation.Context {
		if seen[ref.ID] {
			t.Fatal("duplicate context")
		}
		seen[ref.ID] = true
		evidence, e := service.ReadEvidence(ctx, ref.ID)
		if e != nil {
			t.Fatal(e)
		}
		if evidence.Content == line+"\n" {
			found = true
		}
		if strings.Contains(evidence.Content, "其他不相关") {
			t.Fatal("expanded into unrelated line")
		}
	}
	if !found {
		t.Fatal("lost Windows and temporary constraints from cut original line")
	}
	changed := "更新后的内容"
	if _, err = service.Update(ctx, UpdateInput{ID: created.Information.ID, ExpectedRevision: 1, Content: &changed}); err != nil {
		t.Fatal(err)
	}
	nav, err = service.Navigate(ctx, []string{created.Information.ID}, []string{"related_to"}, 1, 5)
	if err != nil || len(nav.Edges) != 0 {
		t.Fatal("stale relationship context exposed")
	}
}
