package acceptance

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestIndependentSessionBenefitsFromPersistedLesson(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "source")
	first := openTestService(t, dataDir)
	initial, err := first.Search(context.Background(), core.SearchInput{
		Query: "Windows 删除工作目录前应做什么", Contexts: []domain.Context{{Key: "platform", Value: "windows"}},
	})
	if err != nil || len(initial) != 0 {
		t.Fatalf("unexpected initial knowledge: results=%#v err=%v", initial, err)
	}
	created, err := first.Create(context.Background(), core.CreateInput{
		Kind:     domain.KindLesson,
		Content:  "在 Windows 删除工作目录前，必须解析并校验绝对路径仍位于预期工作区内。",
		Contexts: []domain.Context{{Key: "platform", Value: "windows"}},
		Source:   domain.Source{Actor: "external-agent", Ref: "failed-deletion-task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := openTestService(t, dataDir)
	defer second.Close()
	results, err := second.Search(context.Background(), core.SearchInput{
		Query: "删除 Windows 工作目录", Contexts: []domain.Context{{Key: "platform", Value: "windows"}},
	})
	if err != nil || len(results) == 0 || results[0].ID != created.Information.ID {
		t.Fatalf("independent session did not benefit from the lesson: results=%#v err=%v", results, err)
	}
	linuxResults, err := second.Search(context.Background(), core.SearchInput{
		Query: "删除工作目录", Contexts: []domain.Context{{Key: "platform", Value: "linux"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range linuxResults {
		if result.ID == created.Information.ID {
			t.Fatal("a Windows-only lesson leaked into an incompatible Linux context")
		}
	}
}

func TestBackupRestoreAndDerivedRebuildPreserveAuthoritativeAssets(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	source := openTestService(t, sourceDir)
	for index, kind := range domain.Kinds() {
		_, err := source.Create(context.Background(), core.CreateInput{
			Kind: kind, Content: "原样保留的广义个人信息 " + string(rune('甲'+index)),
			Source: domain.Source{Actor: "acceptance", Ref: string(kind)},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	expected := source.store.All()
	backupPath := filepath.Join(root, "assets.ownward")
	if err := source.store.Backup(backupPath); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	restoredDir := filepath.Join(root, "restored")
	if err := assetlog.Restore(backupPath, filepath.Join(restoredDir, "assets")); err != nil {
		t.Fatal(err)
	}
	restored := openTestService(t, restoredDir)
	defer restored.Close()
	if !reflect.DeepEqual(restored.store.All(), expected) {
		t.Fatal("restored authoritative assets differ from the backup source")
	}
	counts, err := restored.service.Maintain(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if counts["pending"] != len(expected) {
		t.Fatalf("derived state was not fully rebuilt: %#v", counts)
	}
	restored.organizeAll(t)
	results, err := restored.service.Search(context.Background(), core.SearchInput{Query: "广义个人信息", Limit: len(expected)})
	if err != nil || len(results) != len(expected) {
		t.Fatalf("restored assets are not searchable: count=%d err=%v", len(results), err)
	}
}

type testService struct {
	service *core.Service
	store   *assetlog.Store
}

func openTestService(t *testing.T, dir string) *testService {
	t.Helper()
	store, err := assetlog.Open(filepath.Join(dir, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(dir, "state"))
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	service, err := core.NewCollaborative(store, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		_ = state.Close()
		_ = store.Close()
		t.Fatal(err)
	}
	return &testService{service: service, store: store}
}

func (s *testService) Close() error {
	return s.service.Close()
}

func (s *testService) Search(ctx context.Context, input core.SearchInput) ([]core.SearchResult, error) {
	return s.service.Search(ctx, input)
}

func (s *testService) Create(ctx context.Context, input core.CreateInput) (core.MutationResult, error) {
	result, err := s.service.Create(ctx, input)
	if err != nil {
		return result, err
	}
	state, err := s.submit(ctx, result.Information.ID)
	result.Organization = state
	return result, err
}

func (s *testService) submit(ctx context.Context, assetID string) (core.OrganizationState, error) {
	works, err := s.service.SemanticWorkFor(ctx, []string{assetID})
	if err != nil || len(works) != 1 {
		return core.OrganizationState{}, err
	}
	work := works[0]
	return s.service.SubmitSemantic(ctx, semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: work.Asset.ID, Revision: work.Asset.Revision,
		Capability: semantics.Capability{ID: "unit-explicit", Version: "v1", Execution: "lifecycle-test"},
		Status:     semantics.SubmissionComplete, Analysis: semantics.Analysis{Summary: work.Asset.Content},
	})
}

func (s *testService) organizeAll(t *testing.T) {
	t.Helper()
	works, err := s.service.SemanticWork(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, work := range works {
		if _, err := s.submit(context.Background(), work.Asset.ID); err != nil {
			t.Fatal(err)
		}
	}
}
