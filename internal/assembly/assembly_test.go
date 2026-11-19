package assembly

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
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
		restore: func(context.Context, string, string, boundedstore.Options) error {
			calls = append(calls, "restore")
			return nil
		},
		openVector: func(string, composition.Manifest) (contract.VectorCapability, error) {
			calls = append(calls, "vector")
			return embedding.Unavailable{}, nil
		},
	}
	_, err := openBoundedWith(Request{
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
	_, err := openBoundedWith(Request{
		DataDir: dataDir, ProductSemantics: Collaborative,
		VectorBundleDir: filepath.Join(t.TempDir(), "embedding"),
	}, manifest, resources{
		restore: func(context.Context, string, string, boundedstore.Options) error {
			calls = append(calls, "restore")
			return nil
		},
		openVector: func(string, composition.Manifest) (contract.VectorCapability, error) {
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

func TestExplicitAssemblyExercisesBoundedModesAndVectorDegradation(t *testing.T) {
	for _, mode := range []ProductSemantics{Basic, Collaborative} {
		t.Run(string(mode), func(t *testing.T) {
			manifest := testManifest(t, mode)
			dataDir := filepath.Join(t.TempDir(), "data")
			request := Request{DataDir: dataDir, ProductSemantics: mode}
			if mode == Collaborative {
				request.VectorBundleDir = filepath.Join(t.TempDir(), "embedding")
			}
			runtime, err := openBoundedWith(request, manifest, resources{
				restore: boundedstore.RestoreLegacyAssetArchive,
				openVector: func(string, composition.Manifest) (contract.VectorCapability, error) {
					return embedding.HashForTesting{Dimensions: 512}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = runtime.Close() })
			ownerToken, err := runtime.UserControl().InitializeOwner("owner")
			if err != nil {
				t.Fatal(err)
			}
			ctx := informationcontrol.Authenticate(context.Background(), ownerToken)
			created, err := runtime.Product().Create(ctx, core.CreateInput{
				Kind: domain.KindGeneral, Content: "装配必须显式且可校验",
				Contexts: []domain.Context{{Key: "scope", Value: "assembly"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			read, err := runtime.Product().Read(ctx, created.Information.ID)
			if err != nil || read.Content != "装配必须显式且可校验" {
				t.Fatalf("unexpected read result: %#v %v", read, err)
			}
			if runtime.ProductSemantics() != mode || runtime.Composition().Composition == "" {
				t.Fatalf("runtime did not retain explicit identity: %#v", runtime.Composition())
			}
			if err := runtime.Close(); err != nil {
				t.Fatal(err)
			}
			if err := runtime.Close(); err != nil {
				t.Fatalf("runtime close was not idempotent: %v", err)
			}
		})
	}

	manifest := testManifest(t, Collaborative)
	degraded, err := openBoundedWith(Request{
		DataDir: filepath.Join(t.TempDir(), "degraded"), ProductSemantics: Collaborative,
		VectorBundleDir: filepath.Join(t.TempDir(), "embedding"),
	}, manifest, resources{
		restore: boundedstore.RestoreLegacyAssetArchive,
		openVector: func(string, composition.Manifest) (contract.VectorCapability, error) {
			return embedding.Unavailable{Reason: "vector unavailable"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer degraded.Close()
	ownerToken, err := degraded.UserControl().InitializeOwner("owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx := informationcontrol.Authenticate(context.Background(), ownerToken)
	res, err := degraded.Product().Create(ctx, core.CreateInput{Kind: domain.KindGeneral, Content: "明确保留既有退化语义"})
	if err != nil || res.Organization.Status != "pending" || !strings.Contains(res.Organization.Error, "vector unavailable") {
		t.Fatalf("unexpected vector degradation state: %#v err=%v", res, err)
	}
}

func TestOrganizedCapabilityMismatchFailsBeforeProductResources(t *testing.T) {
	manifest := testManifest(t, Organized)
	semantic, vector := adaptProvider(failingProvider{})
	dataDir := filepath.Join(t.TempDir(), "product")
	_, err := openBoundedWith(Request{
		DataDir: dataDir, ProductSemantics: Organized,
		OrganizedProvider: semantic, OrganizedVector: vector,
	}, manifest, resources{
		restore: boundedstore.RestoreLegacyAssetArchive,
	})
	if err == nil || !strings.Contains(err.Error(), "语义能力身份") {
		t.Fatalf("mismatched semantic capability was accepted: %v", err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("capability mismatch created product state: %v", err)
	}
}

func TestAssemblyBackupRestoresSQLiteAndLegacyAssetArchives(t *testing.T) {
	manifest := testManifest(t, Basic)
	source := filepath.Join(t.TempDir(), "source")
	runtime, err := openBoundedWith(Request{DataDir: source, ProductSemantics: Basic}, manifest, resources{
		restore: boundedstore.RestoreLegacyAssetArchive,
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, err := runtime.UserControl().InitializeOwner("owner")
	if err != nil {
		t.Fatal(err)
	}
	ctx := informationcontrol.Authenticate(context.Background(), ownerToken)
	created, err := runtime.Product().Create(ctx, core.CreateInput{Kind: domain.KindGeneral, Content: "组合备份必须覆盖权威资产和控制状态。"})
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
	restored, err := openBoundedWith(Request{DataDir: destination, RestoreBackup: backup, ProductSemantics: Basic}, manifest, resources{
		restore: boundedstore.RestoreLegacyAssetArchive,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	reconnectedToken, err := restored.UserControl().RecoverOwner()
	if err != nil {
		t.Fatal(err)
	}
	restoredCtx := informationcontrol.Authenticate(context.Background(), reconnectedToken)
	actual, err := restored.Product().Read(restoredCtx, created.Information.ID)
	if err != nil || actual.Content != created.Information.Content {
		t.Fatalf("restored asset mismatch: %#v %v", actual, err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	controlState, err := ReadControlAt(destination)
	if err != nil || controlState.ActiveComposition != manifest.Identity {
		t.Fatalf("restored control state missing composition identity: %#v %v", controlState, err)
	}

	// Verify legacy ownward.backup/v1 archive restore through assembly.
	legacyZip := filepath.Join(t.TempDir(), "legacy-v1.zip")
	writeLegacyBackupV1Zip(t, legacyZip, domain.Information{
		Schema: domain.AssetSchema, ID: "legacy-1", Revision: 1,
		CreatedAt: time.Unix(1700000000, 0).UTC(), UpdatedAt: time.Unix(1700000000, 0).UTC(),
		Kind: domain.KindGeneral, Content: "早期纯资产归档恢复成功。",
	})
	legacyDest := filepath.Join(t.TempDir(), "legacy-restored")
	legacyRuntime, err := openBoundedWith(Request{DataDir: legacyDest, RestoreBackup: legacyZip, ProductSemantics: Basic}, manifest, resources{
		restore: boundedstore.RestoreLegacyAssetArchive,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer legacyRuntime.Close()
	legacyToken, err := legacyRuntime.UserControl().InitializeOwner("owner")
	if err != nil {
		t.Fatal(err)
	}
	legacyRead, err := legacyRuntime.Product().Read(informationcontrol.Authenticate(context.Background(), legacyToken), "legacy-1")
	if err != nil || legacyRead.Content != "早期纯资产归档恢复成功。" {
		t.Fatalf("legacy backup v1 restore mismatch: %#v %v", legacyRead, err)
	}
}

func writeLegacyBackupV1Zip(t *testing.T, archivePath string, asset domain.Information) {
	t.Helper()
	manifestBytes := []byte("{\"format\":\"ownward.asset-log/v3\",\"created_at\":\"2026-01-01T00:00:00Z\"}\n")
	lineObj := map[string]any{"operation": "create", "recorded_at": asset.CreatedAt, "value": asset}
	lineBytes, err := json.Marshal(lineObj)
	if err != nil {
		t.Fatal(err)
	}
	lineBytes = append(lineBytes, '\n')
	sumM := sha256.Sum256(manifestBytes)
	sumL := sha256.Sum256(lineBytes)
	backupMeta, err := json.Marshal(map[string]any{
		"format": "ownward.backup/v1",
		"files": map[string]string{
			"manifest.json":     hex.EncodeToString(sumM[:]),
			"information.jsonl": hex.EncodeToString(sumL[:]),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, item := range []struct {
		name string
		data []byte
	}{
		{"manifest.json", manifestBytes},
		{"information.jsonl", lineBytes},
		{"backup.json", backupMeta},
	} {
		w, err := zw.Create(item.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(item.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
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
			value.OrganizedProvider, _ = adaptProvider(semantics.Heuristic{})
			return value
		}(),
	} {
		_, err := openBoundedWith(request, manifest, resources{
			restore: boundedstore.RestoreLegacyAssetArchive,
			openVector: func(string, composition.Manifest) (contract.VectorCapability, error) {
				return embedding.Unavailable{}, nil
			},
		})
		if err == nil {
			t.Fatalf("ambiguous or mismatched semantics were accepted: %#v", request)
		}
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

func adaptProvider(provider semantics.Provider) (contract.SemanticCapability, contract.VectorCapability) {
	if provider == nil {
		provider = semantics.Heuristic{}
	}
	return testSemanticCapability{provider: provider}, testVectorCapability{provider: provider}
}

type testSemanticCapability struct{ provider semantics.Provider }

func (value testSemanticCapability) Identity() semantics.Capability {
	return semantics.Capability{ID: value.provider.Name(), Version: "test-provider-v1", Execution: "in-process"}
}

func (value testSemanticCapability) Analyze(ctx context.Context, work semantics.Work) (semantics.Submission, error) {
	analysis, err := value.provider.Analyze(ctx, work.Asset, work.Candidates)
	if err != nil {
		return semantics.Submission{}, err
	}
	return semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: work.Asset.ID,
		Revision: work.Asset.Revision, Capability: value.Identity(), Status: semantics.SubmissionComplete,
		Analysis: analysis,
	}, nil
}

type testVectorCapability struct{ provider semantics.Provider }

func (value testVectorCapability) Name() string { return value.provider.Name() }
func (value testVectorCapability) Space() embedding.Space {
	return embedding.Space{ID: "test:" + strings.TrimSpace(value.provider.Name()), Dimensions: semantics.DefaultEmbeddingDimensions}
}
func (value testVectorCapability) EmbedDocuments(ctx context.Context, inputs []string) ([][]float32, error) {
	return value.provider.Embed(ctx, inputs)
}
func (value testVectorCapability) EmbedQuery(ctx context.Context, input string) ([]float32, error) {
	values, err := value.provider.Embed(ctx, []string{input})
	if err != nil {
		return nil, err
	}
	if len(values) != 1 {
		return nil, errors.New("test vector capability returned an invalid count")
	}
	return values[0], nil
}
func (testVectorCapability) Close() error { return nil }

func testManifest(t *testing.T, mode ProductSemantics) composition.Manifest {
	if mode == Organized {
		semantic, vector := adaptProvider(semantics.Heuristic{})
		return testManifestWithCapabilities(t, mode, semantic, vector)
	}
	return testManifestWithCapabilities(t, mode, nil, nil)
}

func testManifestWithCapabilities(t *testing.T, mode ProductSemantics, semantic contract.SemanticCapability, vector contract.VectorCapability) composition.Manifest {
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
		if role == "semantic" && semantic != nil {
			identity := semantic.Identity()
			config["provider"] = identity.ID
			config["provider_version"] = identity.Version
			config["provider_execution"] = identity.Execution
		}
		if role == "vector" && vector != nil {
			space := vector.Space()
			config["capability"] = vector.Name()
			config["space"] = space.ID
			config["dimensions"] = space.Dimensions
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
