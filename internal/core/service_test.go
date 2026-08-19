package core

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestServiceCreateUpdateAndSearch(t *testing.T) {
	store, err := assetlog.Open(filepath.Join(t.TempDir(), "assets"))
	if err != nil {
		t.Fatal(err)
	}
	service := New(store)
	defer service.Close()
	service.now = func() time.Time { return time.Date(2026, 8, 18, 8, 0, 0, 0, time.UTC) }
	created, err := service.Create(context.Background(), CreateInput{
		Kind:     domain.KindLesson,
		Content:  "在 Windows 删除目录时必须先校验目标路径",
		Contexts: []domain.Context{{Key: "platform", Value: "windows"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.Search(context.Background(), SearchInput{Query: "删除 Windows 目录", Contexts: []domain.Context{{Key: "platform", Value: "windows"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != created.Information.ID {
		t.Fatalf("unexpected results: %#v", results)
	}
	updatedContent := "在 Windows 删除目录时必须先解析并校验绝对目标路径"
	updated, err := service.Update(context.Background(), UpdateInput{ID: created.Information.ID, ExpectedRevision: 1, Content: &updatedContent})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Information.Revision != 2 {
		t.Fatalf("unexpected revision: %d", updated.Information.Revision)
	}
}

func TestServiceNavigatesSemanticRelations(t *testing.T) {
	root := t.TempDir()
	store, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	derivedStore, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewOrganized(store, derivedStore, semantics.Heuristic{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	parent, err := service.Create(context.Background(), CreateInput{Kind: domain.KindKnowledge, Content: "React 的工作原理"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.Create(context.Background(), CreateInput{
		Kind: domain.KindKnowledge, Content: "React 并发渲染",
		Relations: []domain.ExplicitRelation{{Type: "part_of", TargetID: parent.Information.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Navigate(context.Background(), []string{child.Information.ID}, []string{"part_of"}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Edges) != 1 || result.Edges[0].TargetID != parent.Information.ID || len(result.Nodes) != 2 {
		t.Fatalf("unexpected navigation result: %#v", result)
	}
}

func TestSearchDoesNotForceContextOnGeneralInformation(t *testing.T) {
	store, err := assetlog.Open(filepath.Join(t.TempDir(), "assets"))
	if err != nil {
		t.Fatal(err)
	}
	service := New(store)
	defer service.Close()
	_, err = service.Create(context.Background(), CreateInput{Kind: domain.KindThought, Content: "信息资产应当长期属于用户"})
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.Search(context.Background(), SearchInput{Query: "信息资产属于谁"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestSearchByStableIdentityDoesNotCallTheSemanticProvider(t *testing.T) {
	root := t.TempDir()
	store, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &relationProvider{}
	service, err := NewOrganized(store, state, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	created, err := service.Create(context.Background(), CreateInput{Content: "identity lookup"})
	if err != nil {
		t.Fatal(err)
	}
	provider.embedCalls = 0
	results, err := service.Search(context.Background(), SearchInput{Query: created.Information.ID})
	if err != nil || len(results) != 1 || results[0].ID != created.Information.ID {
		t.Fatalf("unexpected identity result: results=%#v err=%v", results, err)
	}
	if provider.embedCalls != 0 {
		t.Fatalf("identity lookup unnecessarily called the semantic provider %d times", provider.embedCalls)
	}
}

func TestSearchKeepsDirectEvidenceAheadOfAccumulatedRelations(t *testing.T) {
	root := t.TempDir()
	store, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewOrganized(store, state, fusionProvider{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	target, err := service.Create(context.Background(), CreateInput{Content: "unrelated target"})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := service.Create(context.Background(), CreateInput{Content: "needle needle needle exact answer"})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		_, err = service.Create(context.Background(), CreateInput{
			Content:   "needle lexical source " + string(rune('a'+index)),
			Relations: []domain.ExplicitRelation{{Type: "related_to", TargetID: target.Information.ID}},
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.Create(context.Background(), CreateInput{
			Content:   "semantic source " + string(rune('a'+index)),
			Relations: []domain.ExplicitRelation{{Type: "related_to", TargetID: target.Information.ID}},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	results, err := service.Search(context.Background(), SearchInput{Query: "needle", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].ID != direct.Information.ID {
		t.Fatalf("accumulated relation evidence displaced the direct answer: %#v", results)
	}
}

func TestSearchPreservesRelationEvidenceBetweenDirectSeeds(t *testing.T) {
	root := t.TempDir()
	store, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewOrganized(store, state, fusionProvider{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	target, err := service.Create(context.Background(), CreateInput{Content: "needle target"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.Create(context.Background(), CreateInput{
		Content: "needle source", Relations: []domain.ExplicitRelation{{Type: "supports", TargetID: target.Information.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), CreateInput{Content: "needle distractor"}); err != nil {
		t.Fatal(err)
	}
	results, err := service.Search(context.Background(), SearchInput{Query: "needle", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, result := range results {
		if result.ID == source.Information.ID || result.ID == target.Information.ID {
			seen[result.ID] = contains(result.Signals, "relation")
		}
	}
	if !seen[source.Information.ID] || !seen[target.Information.ID] {
		t.Fatalf("direct seeds lost their relation evidence: %#v", results)
	}
}

func TestServicePreservesExplicitRelationsAndRefreshesStaleInferences(t *testing.T) {
	root := t.TempDir()
	store, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	derivedStore, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &relationProvider{}
	service, err := NewOrganized(store, derivedStore, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	target, err := service.Create(context.Background(), CreateInput{Content: "target"})
	if err != nil {
		t.Fatal(err)
	}
	provider.targetID = target.Information.ID
	inferred, err := service.Create(context.Background(), CreateInput{Content: "source"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Create(context.Background(), CreateInput{
		Content: "explicit", Relations: []domain.ExplicitRelation{{Type: "supports", TargetID: target.Information.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := service.Navigate(context.Background(), []string{target.Information.ID}, nil, 1, 10)
	if err != nil || len(before.Edges) != 2 {
		t.Fatalf("unexpected initial relations: result=%#v err=%v", before, err)
	}

	updatedContent := "target changed"
	if _, err := service.Update(context.Background(), UpdateInput{ID: target.Information.ID, ExpectedRevision: 1, Content: &updatedContent}); err != nil {
		t.Fatal(err)
	}
	afterUpdate, err := service.Navigate(context.Background(), []string{target.Information.ID}, nil, 1, 10)
	if err != nil || len(afterUpdate.Edges) != 2 {
		t.Fatalf("inferred relation was not refreshed after target update: result=%#v err=%v", afterUpdate, err)
	}

	counts, err := service.Maintain(context.Background(), false)
	if err != nil || counts["ready"] != 0 || counts["unchanged"] != 3 {
		t.Fatalf("unexpected maintenance result: counts=%#v err=%v", counts, err)
	}
	afterMaintenance, err := service.Navigate(context.Background(), []string{target.Information.ID}, nil, 1, 10)
	if err != nil || len(afterMaintenance.Edges) != 2 {
		t.Fatalf("inferred relation was not refreshed: result=%#v err=%v", afterMaintenance, err)
	}
	_ = inferred
}

func TestServiceAppliesAndRemovesRelationsInferredTowardCurrentInformation(t *testing.T) {
	root := t.TempDir()
	store, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewOrganized(store, state, incomingRelationProvider{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	older, err := service.Create(context.Background(), CreateInput{Content: "older path"})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := service.Create(context.Background(), CreateInput{Content: "new principle"})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := service.Navigate(context.Background(), []string{older.Information.ID}, nil, 1, 10)
	if err != nil || len(graph.Edges) != 1 || graph.Edges[0].SourceID != older.Information.ID || graph.Edges[0].TargetID != newer.Information.ID {
		t.Fatalf("incoming inference was not materialized in its semantic direction: graph=%#v err=%v", graph, err)
	}
	if record, exists := state.GetWithEmbedding(older.Information.ID); !exists || len(record.Embedding) != 2 {
		t.Fatalf("updating an incoming relation discarded the source embedding: %#v", record)
	}
	updatedContent := "new principle changed"
	if _, err := service.Update(context.Background(), UpdateInput{
		ID: newer.Information.ID, ExpectedRevision: newer.Information.Revision, Content: &updatedContent,
	}); err != nil {
		t.Fatal(err)
	}
	graph, err = service.Navigate(context.Background(), []string{older.Information.ID}, nil, 1, 10)
	if err != nil || len(graph.Edges) != 0 {
		t.Fatalf("obsolete incoming inference was not removed: graph=%#v err=%v", graph, err)
	}
	if record, exists := state.GetWithEmbedding(older.Information.ID); !exists || len(record.Embedding) != 2 {
		t.Fatalf("removing an incoming relation discarded the source embedding: %#v", record)
	}
}

func TestUpdateOrganizesAssetAndDependentsConcurrently(t *testing.T) {
	root := t.TempDir()
	store, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &concurrentOrganizationProvider{release: make(chan struct{}), allStarted: make(chan struct{})}
	service, err := NewOrganized(store, state, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	target, err := service.Create(context.Background(), CreateInput{Content: "target"})
	if err != nil {
		t.Fatal(err)
	}
	provider.targetID = target.Information.ID
	for _, content := range []string{"source-a", "source-b"} {
		if _, err = service.Create(context.Background(), CreateInput{Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	provider.block.Store(true)
	updatedContent := "target changed"
	done := make(chan error, 1)
	go func() {
		_, updateErr := service.Update(context.Background(), UpdateInput{
			ID: target.Information.ID, ExpectedRevision: 1, Content: &updatedContent,
		})
		done <- updateErr
	}()
	select {
	case <-provider.allStarted:
		close(provider.release)
	case <-time.After(2 * time.Second):
		close(provider.release)
		t.Fatal("asset and dependent organization did not overlap")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServiceNeverLoadsDerivedStateForAnOlderAssetRevision(t *testing.T) {
	root := t.TempDir()
	store, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &relationProvider{}
	service, err := NewOrganized(store, state, provider)
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.Create(context.Background(), CreateInput{Content: "target"})
	if err != nil {
		t.Fatal(err)
	}
	provider.targetID = target.Information.ID
	source, err := service.Create(context.Background(), CreateInput{Content: "source"})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	stale, _ := store.Get(source.Information.ID)
	stale.Revision++
	stale.UpdatedAt = stale.UpdatedAt.Add(time.Second)
	stale.Content = "source changed after the last derived write"
	if err := store.Update(stale, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err = derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err = NewOrganized(store, state, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	result, err := service.Navigate(context.Background(), []string{source.Information.ID}, nil, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Edges) != 0 {
		t.Fatalf("stale derived relation became visible: %#v", result.Edges)
	}
}

func TestConcurrentUpdatesCannotOverwriteTheSameRevision(t *testing.T) {
	root := t.TempDir()
	store, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewOrganized(store, state, semantics.Heuristic{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	created, err := service.Create(context.Background(), CreateInput{Content: "original"})
	if err != nil {
		t.Fatal(err)
	}
	contents := []string{"first", "second"}
	errorsByUpdate := make([]error, len(contents))
	var wait sync.WaitGroup
	for index := range contents {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, errorsByUpdate[index] = service.Update(context.Background(), UpdateInput{
				ID: created.Information.ID, ExpectedRevision: 1, Content: &contents[index],
			})
		}(index)
	}
	wait.Wait()
	succeeded := 0
	for _, updateErr := range errorsByUpdate {
		if updateErr == nil {
			succeeded++
		}
	}
	current, ok := store.Get(created.Information.ID)
	if succeeded != 1 || !ok || current.Revision != 2 {
		t.Fatalf("concurrent updates violated optimistic concurrency: successes=%d value=%#v errors=%#v", succeeded, current, errorsByUpdate)
	}
	record, ok := state.Get(created.Information.ID)
	if !ok || record.AssetRevision != current.Revision {
		t.Fatalf("derived state does not match the winning revision: %#v", record)
	}
}

type relationProvider struct {
	targetID   string
	embedCalls int
}

type fusionProvider struct{}

type incomingRelationProvider struct{}

type concurrentOrganizationProvider struct {
	targetID   string
	block      atomic.Bool
	active     atomic.Int32
	started    sync.Once
	allStarted chan struct{}
	release    chan struct{}
}

func (fusionProvider) Name() string { return "test-fusion-provider" }

func (incomingRelationProvider) Name() string { return "test-incoming-relation-provider" }

func (incomingRelationProvider) Embed(_ context.Context, values []string) ([][]float32, error) {
	result := make([][]float32, len(values))
	for index := range values {
		result[index] = []float32{1, 0}
	}
	return result, nil
}

func (incomingRelationProvider) Analyze(_ context.Context, value domain.Information, candidates []semantics.Candidate) (semantics.Analysis, error) {
	analysis := semantics.Analysis{Kind: domain.KindKnowledge, Summary: value.Content}
	if value.Content == "new principle" && len(candidates) > 0 {
		analysis.Relations = []semantics.Relation{{
			Type: "related_to", TargetID: candidates[0].ID, Confidence: 1, Direction: "incoming",
		}}
	}
	return analysis, nil
}

func (fusionProvider) Embed(_ context.Context, values []string) ([][]float32, error) {
	result := make([][]float32, len(values))
	for index, value := range values {
		if value == "needle" || strings.HasPrefix(value, "semantic source") {
			result[index] = []float32{1, 0}
		} else {
			result[index] = []float32{0, 1}
		}
	}
	return result, nil
}

func (fusionProvider) Analyze(_ context.Context, value domain.Information, _ []semantics.Candidate) (semantics.Analysis, error) {
	relations := make([]semantics.Relation, 0, len(value.Relations))
	for _, relation := range value.Relations {
		relations = append(relations, semantics.Relation{Type: relation.Type, TargetID: relation.TargetID, Confidence: 1})
	}
	return semantics.Analysis{Kind: value.Kind, Summary: value.Content, Relations: relations}, nil
}

func (p *concurrentOrganizationProvider) Name() string {
	return "test-concurrent-organization-provider"
}

func (p *concurrentOrganizationProvider) Embed(_ context.Context, values []string) ([][]float32, error) {
	result := make([][]float32, len(values))
	for index := range values {
		result[index] = []float32{1, 0}
	}
	return result, nil
}

func (p *concurrentOrganizationProvider) Analyze(_ context.Context, value domain.Information, _ []semantics.Candidate) (semantics.Analysis, error) {
	if p.block.Load() {
		if p.active.Add(1) == 3 {
			p.started.Do(func() { close(p.allStarted) })
		}
		<-p.release
		p.active.Add(-1)
	}
	analysis := semantics.Analysis{Kind: value.Kind, Summary: value.Content}
	if strings.HasPrefix(value.Content, "source-") {
		analysis.Relations = []semantics.Relation{{Type: "related_to", TargetID: p.targetID, Confidence: 1}}
	}
	return analysis, nil
}

func (p *relationProvider) Name() string { return "test-relation-provider" }

func (p *relationProvider) Embed(_ context.Context, values []string) ([][]float32, error) {
	p.embedCalls++
	result := make([][]float32, len(values))
	for index := range values {
		result[index] = []float32{1, 0}
	}
	return result, nil
}

func (p *relationProvider) Analyze(_ context.Context, value domain.Information, _ []semantics.Candidate) (semantics.Analysis, error) {
	analysis := semantics.Analysis{Kind: value.Kind, Summary: value.Content}
	if value.Content == "source" {
		analysis.Relations = []semantics.Relation{{Type: "related_to", TargetID: p.targetID, Confidence: 0.95}}
	}
	return analysis, nil
}
