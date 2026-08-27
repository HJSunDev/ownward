package assembly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestInvalidCompositionFailsBeforeEveryRuntimeSideEffect(t *testing.T) {
	manifest := testManifest(t, Collaborative)
	manifest.Components[0].Content[0].SHA256 = strings.Repeat("f", 64)
	dataDir := filepath.Join(t.TempDir(), "data")
	acceptance := filepath.Join(t.TempDir(), "acceptance-state.json")
	writeAbsoluteFile(t, acceptance, "unchanged")
	calls := []string{}
	resource := resources{
		restore: func(_, _ string, _ contract.ControlState) error { calls = append(calls, "restore"); return nil },
		openAuthority: func(string, contract.ControlState) (contract.AuthoritySubstrate, error) {
			calls = append(calls, "assets")
			return nil, errors.New("must not open")
		},
		openVector: func(string, composition.Manifest) (embedding.Provider, error) {
			calls = append(calls, "vector")
			return embedding.Unavailable{}, nil
		},
	}
	_, err := openWith(Request{
		DataDir: dataDir, RestoreBackup: filepath.Join(t.TempDir(), "backup"), ProductSemantics: Collaborative,
		VectorBundleDir: filepath.Join(t.TempDir(), "embedding"),
	}, manifest, resource)
	if err == nil || !strings.Contains(err.Error(), "身份漂移") {
		t.Fatalf("content drift was not rejected: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("invalid composition reached runtime resources: %v", calls)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("invalid composition created product state: %v", err)
	}
	if value, err := os.ReadFile(acceptance); err != nil || string(value) != "unchanged" {
		t.Fatalf("invalid composition changed unrelated control/evidence state: %v %q", err, value)
	}
}

func TestInvalidPackagedVectorFailsBeforeProductResources(t *testing.T) {
	manifest := testManifest(t, Collaborative)
	dataDir := filepath.Join(t.TempDir(), "data")
	calls := []string{}
	_, err := openWith(Request{
		DataDir: dataDir, ProductSemantics: Collaborative,
		VectorBundleDir: filepath.Join(t.TempDir(), "embedding"),
	}, manifest, resources{
		restore: func(_, _ string, _ contract.ControlState) error { calls = append(calls, "restore"); return nil },
		openAuthority: func(string, contract.ControlState) (contract.AuthoritySubstrate, error) {
			calls = append(calls, "assets")
			return nil, errors.New("must not open")
		},
		openVector: func(string, composition.Manifest) (embedding.Provider, error) {
			calls = append(calls, "vector")
			return nil, errors.New("packaged vector identity mismatch")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("invalid packaged vector was accepted: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"vector"}) {
		t.Fatalf("invalid packaged vector reached product resources: %v", calls)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("invalid packaged vector created product state: %v", err)
	}
}

func TestSharedConnectorPreflightDoesNotHashTechnicalArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "embedding")
	modelPath := "model/model.gguf"
	runtimePath := "runtime/llama-server.exe"
	writeAbsoluteFile(t, filepath.Join(root, filepath.FromSlash(modelPath)), "tampered-model")
	writeAbsoluteFile(t, filepath.Join(root, filepath.FromSlash(runtimePath)), "runtime")
	digest := func(value string) string {
		sum := sha256.Sum256([]byte(value))
		return hex.EncodeToString(sum[:])
	}
	bundleManifest := embedding.Manifest{
		Schema: embedding.ManifestSchema, Capability: "test-capability",
		Model: embedding.ModelArtifact{Path: modelPath, SHA256: digest("expected-model")},
		Runtime: embedding.RuntimeArtifact{
			Entry: runtimePath, SourceArchiveSHA256: digest("runtime-archive"),
			Files: map[string]string{runtimePath: digest("runtime")},
		},
		Space: embedding.SpaceDefinition{
			Dimensions: 512, SourceDimensions: 768, QueryPrefix: "query: ", DocumentPrefix: "document: ",
			Pooling: "mean", Normalization: "l2", Truncation: "prefix",
		},
	}
	spaceID, err := embedding.ComputeSpaceID(bundleManifest)
	if err != nil {
		t.Fatal(err)
	}
	bundleManifest.Space.ID = spaceID
	encoded, err := json.Marshal(bundleManifest)
	if err != nil {
		t.Fatal(err)
	}
	writeAbsoluteFile(t, filepath.Join(root, "manifest.json"), string(encoded))
	manifestDigest, err := fileSHA256(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(t, Collaborative)
	for index := range manifest.Components {
		if manifest.Components[index].Role != "vector" {
			continue
		}
		manifest.Components[index].Content = append(manifest.Components[index].Content, composition.Content{Name: "manifest.json", SHA256: manifestDigest})
		manifest.Components[index].Config = map[string]any{
			"bundle_schema": embedding.ManifestSchema,
			"capability":    bundleManifest.Capability,
			"space":         bundleManifest.Space.ID,
			"dimensions":    bundleManifest.Space.Dimensions,
		}
	}

	if _, err := inspectProductionVector(root, manifest); err != nil {
		t.Fatalf("lightweight connector preflight read full model content: %v", err)
	}
	if _, err := openProductionVector(root, manifest); err == nil || !strings.Contains(err.Error(), "校验向量模型") {
		t.Fatalf("runtime owner did not perform full technical verification: %v", err)
	}
}

func TestExplicitAssemblyMatchesAllLegacyProductSemantics(t *testing.T) {
	for _, mode := range []ProductSemantics{Basic, Organized, Collaborative} {
		t.Run(string(mode), func(t *testing.T) {
			manifest := testManifest(t, mode)
			oldDir := filepath.Join(t.TempDir(), "old")
			newDir := filepath.Join(t.TempDir(), "new")
			legacy := openLegacy(t, mode, oldDir, semantics.Heuristic{}, embedding.HashForTesting{Dimensions: 32})
			t.Cleanup(func() { _ = legacy.Close() })
			request := Request{
				DataDir: newDir, ProductSemantics: mode,
			}
			if mode == Organized {
				request.OrganizedProvider = semantics.Heuristic{}
			}
			if mode == Collaborative {
				request.VectorBundleDir = filepath.Join(t.TempDir(), "embedding")
			}
			runtime, err := openWith(request, manifest, resources{
				restore: authoritysubstrate.Restore, openAuthority: openTestAuthority,
				openVector: func(string, composition.Manifest) (embedding.Provider, error) {
					return embedding.HashForTesting{Dimensions: 32}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runtime.Close() })
			legacySnapshot := exerciseProduct(t, legacy)
			assembledSnapshot := exerciseProduct(t, runtime.Service())
			if !reflect.DeepEqual(legacySnapshot, assembledSnapshot) {
				t.Fatalf("new assembly changed %s semantics:\nlegacy=%#v\nassembled=%#v", mode, legacySnapshot, assembledSnapshot)
			}
			if runtime.ProductSemantics() != mode || runtime.Composition().Composition == "" {
				t.Fatalf("runtime did not retain explicit identity: %#v", runtime.Composition())
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}
			if err := runtime.Close(); err != nil {
				t.Fatal(err)
			}
			if err := runtime.Close(); err != nil {
				t.Fatalf("runtime close was not idempotent: %v", err)
			}
			assertPersistedEquivalent(t, oldDir, newDir, legacySnapshot.Content)
		})
	}
}

func TestExplicitAssemblyPreservesOrganizedAndVectorDegradation(t *testing.T) {
	for _, test := range []struct {
		name     string
		mode     ProductSemantics
		provider semantics.Provider
		vector   embedding.Provider
	}{
		{name: "organized", mode: Organized, provider: failingProvider{}},
		{name: "collaborative", mode: Collaborative, vector: embedding.Unavailable{Reason: "vector unavailable"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := testManifest(t, test.mode)
			legacy := openLegacy(t, test.mode, filepath.Join(t.TempDir(), "legacy"), test.provider, test.vector)
			defer legacy.Close()
			request := Request{DataDir: filepath.Join(t.TempDir(), "new"), ProductSemantics: test.mode}
			if test.mode == Organized {
				request.OrganizedProvider = test.provider
			} else {
				request.VectorBundleDir = filepath.Join(t.TempDir(), "embedding")
			}
			runtime, err := openWith(request, manifest, resources{
				restore: authoritysubstrate.Restore, openAuthority: openTestAuthority,
				openVector: func(string, composition.Manifest) (embedding.Provider, error) { return test.vector, nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			legacyResult, legacyErr := legacy.Create(context.Background(), core.CreateInput{Kind: domain.KindGeneral, Content: "明确保留既有退化语义"})
			newResult, newErr := runtime.Service().Create(context.Background(), core.CreateInput{Kind: domain.KindGeneral, Content: "明确保留既有退化语义"})
			if errorText(legacyErr) != errorText(newErr) || legacyResult.Organization.Status != newResult.Organization.Status || legacyResult.Organization.Error != newResult.Organization.Error || legacyResult.Organization.RequiredAction != newResult.Organization.RequiredAction {
				t.Fatalf("degradation changed: legacy=%#v/%v new=%#v/%v", legacyResult, legacyErr, newResult, newErr)
			}
		})
	}
}

func TestAssemblyBackupRestoresAssetsAndControlTogether(t *testing.T) {
	manifest := testManifest(t, Basic)
	source := filepath.Join(t.TempDir(), "source")
	runtime, err := openWith(Request{DataDir: source, ProductSemantics: Basic}, manifest, resources{
		restore: authoritysubstrate.Restore, openAuthority: openTestAuthority,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.Service().Create(context.Background(), core.CreateInput{Kind: domain.KindGeneral, Content: "组合备份必须覆盖权威资产和控制状态。"})
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "authority.ownward")
	if err := runtime.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	restored, err := openWith(Request{DataDir: destination, RestoreBackup: backup, ProductSemantics: Basic}, manifest, resources{
		restore: authoritysubstrate.Restore, openAuthority: openTestAuthority,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	actual, err := restored.Service().Read(context.Background(), created.Information.ID)
	if err != nil || actual.Content != created.Information.Content {
		t.Fatalf("restored asset mismatch: %#v %v", actual, err)
	}
	control, err := os.ReadFile(filepath.Join(destination, "authority", "control.json"))
	if err != nil || !strings.Contains(string(control), manifest.Identity) {
		t.Fatalf("restored control state missing composition identity: %v", err)
	}
}

func TestExplicitModeRejectsNilAndManifestMismatchBeforeOpen(t *testing.T) {
	manifest := testManifest(t, Collaborative)
	base := Request{DataDir: filepath.Join(t.TempDir(), "data")}
	for _, request := range []Request{
		base,
		func() Request { value := base; value.ProductSemantics = Organized; return value }(),
		func() Request {
			value := base
			value.ProductSemantics = Basic
			value.VectorBundleDir = filepath.Join(t.TempDir(), "vector")
			return value
		}(),
		func() Request {
			value := base
			value.ProductSemantics = Organized
			value.OrganizedProvider = semantics.Heuristic{}
			return value
		}(),
	} {
		_, err := openWith(request, manifest, resources{
			restore: authoritysubstrate.Restore, openAuthority: func(string, contract.ControlState) (contract.AuthoritySubstrate, error) {
				t.Fatal("opened assets")
				return nil, nil
			},
			openVector: func(string, composition.Manifest) (embedding.Provider, error) {
				return embedding.Unavailable{}, nil
			},
		})
		if err == nil {
			t.Fatalf("ambiguous or mismatched semantics were accepted: %#v", request)
		}
	}
}

type productSnapshot struct {
	Rules           string
	CreateStatus    string
	UpdateStatus    string
	Revision        uint64
	Content         string
	SearchCount     int
	SearchSummary   string
	SearchSignals   []string
	NavigationNodes int
	NavigationEdges int
	NavigationError string
	Maintain        map[string]int
	MaintainError   string
	Rebuild         map[string]int
	RebuildError    string
}

func exerciseProduct(t *testing.T, service *core.Service) productSnapshot {
	t.Helper()
	ctx := context.Background()
	created, err := service.Create(ctx, core.CreateInput{Kind: domain.KindGeneral, Content: "装配必须显式且可校验", Contexts: []domain.Context{{Key: "scope", Value: "assembly"}}})
	if err != nil {
		t.Fatal(err)
	}
	updatedContent := "装配必须在资源打开前完成校验"
	updated, err := service.Update(ctx, core.UpdateInput{ID: created.Information.ID, ExpectedRevision: 1, Content: &updatedContent})
	if err != nil {
		t.Fatal(err)
	}
	read, err := service.Read(ctx, created.Information.ID)
	if err != nil {
		t.Fatal(err)
	}
	search, err := service.Search(ctx, core.SearchInput{Query: "资源打开前 校验", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	navigation, navigationErr := service.Navigate(ctx, []string{created.Information.ID}, nil, 1, 10)
	maintain, maintainErr := service.Maintain(ctx, false)
	rebuild, rebuildErr := service.Maintain(ctx, true)
	signals := []string(nil)
	summary := ""
	if len(search) > 0 {
		signals = append(signals, search[0].Signals...)
		sort.Strings(signals)
		summary = search[0].Summary
	}
	return productSnapshot{
		Rules: service.Rules(ctx), CreateStatus: created.Organization.Status, UpdateStatus: updated.Organization.Status,
		Revision: read.Revision, Content: read.Content, SearchCount: len(search), SearchSummary: summary, SearchSignals: signals,
		NavigationNodes: len(navigation.Nodes), NavigationEdges: len(navigation.Edges), NavigationError: errorText(navigationErr), Maintain: maintain,
		MaintainError: errorText(maintainErr), Rebuild: rebuild, RebuildError: errorText(rebuildErr),
	}
}

func openLegacy(t *testing.T, mode ProductSemantics, dataDir string, provider semantics.Provider, vector embedding.Provider) *core.Service {
	t.Helper()
	store, err := assetlog.Open(filepath.Join(dataDir, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	if mode == Basic {
		return core.New(store)
	}
	derivedStore, err := derived.Open(filepath.Join(dataDir, "state"))
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	var service *core.Service
	if mode == Organized {
		service, err = core.NewOrganized(store, derivedStore, provider)
	} else {
		service, err = core.NewCollaborative(store, derivedStore, vector)
	}
	if err != nil {
		_ = derivedStore.Close()
		_ = store.Close()
		t.Fatal(err)
	}
	return service
}

func openTestAuthority(path string, initial contract.ControlState) (contract.AuthoritySubstrate, error) {
	return authoritysubstrate.Open(path, initial)
}

func assertPersistedEquivalent(t *testing.T, oldDir, newDir, expected string) {
	t.Helper()
	oldStore, err := assetlog.Open(filepath.Join(oldDir, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer oldStore.Close()
	newStore, err := assetlog.Open(filepath.Join(newDir, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer newStore.Close()
	oldAssets, newAssets := oldStore.All(), newStore.All()
	if len(oldAssets) != 1 || len(newAssets) != 1 || oldAssets[0].Content != expected || newAssets[0].Content != expected || oldAssets[0].Revision != newAssets[0].Revision {
		t.Fatalf("persisted authority differs: old=%#v new=%#v", oldAssets, newAssets)
	}
}

type failingProvider struct{}

func (failingProvider) Name() string { return "failing-provider" }
func (failingProvider) Analyze(context.Context, domain.Information, []semantics.Candidate) (semantics.Analysis, error) {
	return semantics.Analysis{}, errors.New("semantic unavailable")
}
func (failingProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("semantic unavailable")
}

func testManifest(t *testing.T, mode ProductSemantics) composition.Manifest {
	t.Helper()
	repository := t.TempDir()
	for _, definition := range contract.Definitions() {
		writeTestFile(t, repository, definition.Source, definition.ID+"/v1")
	}
	components := make([]composition.Component, 0, 7)
	for _, role := range []string{"authority-substrate", "semantic", "vector", "product-rules", "kernel", "access", "assembly"} {
		path := filepath.ToSlash(filepath.Join("components", role+".txt"))
		writeTestFile(t, repository, path, role+"-v1")
		config := map[string]any{"test_role": role}
		if role == "kernel" {
			config["mode"] = string(mode)
		}
		if role == "assembly" {
			config["product_semantics"] = string(mode)
			config["entry"] = activeAssemblyEntry
			config["activation"] = activeAssemblyActivation
		}
		components = append(components, composition.Component{Role: role, Content: []composition.Content{{Name: role, Path: path}}, Config: config})
	}
	sealed, err := composition.Seal(repository, composition.Manifest{Schema: composition.ManifestSchema, Name: "test-" + string(mode), Components: components})
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func writeTestFile(t *testing.T, root, relative, value string) {
	t.Helper()
	writeAbsoluteFile(t, filepath.Join(root, filepath.FromSlash(relative)), value)
}

func writeAbsoluteFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
