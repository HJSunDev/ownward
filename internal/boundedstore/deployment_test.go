package boundedstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func deploymentOptions() DeploymentOptions {
	b, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
	return DeploymentOptions{Options: Options{Budget: b}}
}

func assertLegacyManifestFormat(t *testing.T, root, wantFormat string) {
	t.Helper()
	var m struct {
		Format string `json:"format"`
	}
	if err := readSmallJSON(filepath.Join(root, "assets", "manifest.json"), &m); err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	if m.Format != wantFormat {
		t.Fatalf("manifest format = %q, want %q", m.Format, wantFormat)
	}
}

func writeLegacyControlEnvelope(t *testing.T, root string, state contract.ControlState) {
	t.Helper()
	stateBytes, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := compactDigest(bytes.NewReader(stateBytes))
	if err != nil {
		t.Fatal(err)
	}
	envelope := struct {
		Schema string          `json:"schema"`
		SHA256 string          `json:"sha256"`
		State  json.RawMessage `json:"state"`
	}{
		Schema: "ownward.control-state-envelope/v1",
		SHA256: digest,
		State:  stateBytes,
	}
	if err := os.MkdirAll(filepath.Join(root, "authority"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeDurableJSON(filepath.Join(root, "authority", "control.json"), envelope); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyDerivedGeneration(t *testing.T, root, gen string, record derived.Record) {
	t.Helper()
	vectorFloats := record.Embedding
	metaRecord := record
	metaRecord.Schema = "ownward.derived/v5"
	metaRecord.Embedding = nil
	metaBytes, err := json.Marshal(metaRecord)
	if err != nil {
		t.Fatal(err)
	}
	vecBytes := make([]byte, len(vectorFloats)*4)
	for i, v := range vectorFloats {
		binary.LittleEndian.PutUint32(vecBytes[i*4:], math.Float32bits(v))
	}
	crc := crc32.NewIEEE()
	_, _ = crc.Write(metaBytes)
	_, _ = crc.Write(vecBytes)
	var frame bytes.Buffer
	frame.WriteString("OWD3")
	var lens [12]byte
	binary.LittleEndian.PutUint32(lens[0:4], uint32(len(metaBytes)))
	binary.LittleEndian.PutUint32(lens[4:8], uint32(len(vecBytes)))
	binary.LittleEndian.PutUint32(lens[8:12], crc.Sum32())
	frame.Write(lens[:])
	frame.Write(metaBytes)
	frame.Write(vecBytes)
	frame.WriteString("DONE")

	genDir := filepath.Join(root, "state", "generations", gen)
	if err := os.MkdirAll(genDir, 0700); err != nil {
		t.Fatal(err)
	}
	logBytes := frame.Bytes()
	if err := os.WriteFile(filepath.Join(genDir, "organization.binlog"), logBytes, 0600); err != nil {
		t.Fatal(err)
	}
	logSum := sha256.Sum256(logBytes)
	manifestBytes, err := json.Marshal(map[string]any{
		"schema":       "ownward.derived-generation/v1",
		"generation":   gen,
		"sealed_bytes": len(logBytes),
		"log_sha256":   hex.EncodeToString(logSum[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(genDir, "manifest.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0600); err != nil {
		t.Fatal(err)
	}
	manifestSum := sha256.Sum256(manifestBytes)
	if err := writeDurableJSON(filepath.Join(root, "state", "current.json"), map[string]any{
		"schema":          "ownward.derived-current/v1",
		"generation":      gen,
		"manifest_sha256": hex.EncodeToString(manifestSum[:]),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentIdentityDoesNotTakeActiveWriterLock(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "资料 #1")
	o := deploymentOptions()
	o.Initialize = func(ctx context.Context, s *Store, _ string) error {
		a, err := s.OpenControlAuthority(ctx, contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "composition", ActiveKernelGeneration: "kernel"})
		if err != nil {
			return err
		}
		_, err = informationcontrol.New(a).InitializeOwner("owner")
		return err
	}
	s, err := OpenDeployment(ctx, root, o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 2; i++ {
		state, err := ReadDeploymentIdentity(ctx, root)
		if err != nil || state.InformationControl == nil || state.InformationControl.SystemID == "" || state.ActiveComposition != "composition" {
			t.Fatal(state, err)
		}
		if len(state.InformationControl.Principals) != 0 {
			t.Fatal("identity inspection returned credentials")
		}
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err = ReadDeploymentIdentity(ctx, missing); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err = os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("inspection created missing data", err)
	}
}

func legacyFixture(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "assets")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := writeDurableJSON(filepath.Join(dir, "manifest.json"), map[string]any{"format": "ownward.asset-log/v3", "created_at": time.Now().UTC()}); e != nil {
		t.Fatal(e)
	}
	body := "  " + strings.Repeat("原文🙂\n引号\"\\", 14000) + "\r\n"
	now := time.Unix(100, 0).UTC()
	v := domain.Information{Schema: domain.AssetSchema, ID: "asset", Revision: 4, CreatedAt: now, UpdatedAt: now, Kind: domain.KindKnowledge, Content: body, Contexts: []domain.Context{{Key: "项目", Value: "长期资料"}}}
	f, e := os.Create(filepath.Join(dir, "information.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.NewEncoder(f).Encode(map[string]any{"operation": "snapshot", "value": v}); e != nil {
		t.Fatal(e)
	}
	f.Close()
	return body
}

func TestDeploymentResumesEveryDurableBoundary(t *testing.T) {
	for _, phase := range []string{"intent", "isolated-marker", "validated", "pointer", "activated", "cleaned"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			body := legacyFixture(t, root)
			o := deploymentOptions()
			interrupted := errors.New("power loss")
			o.checkpoint = func(p string) error {
				if p == phase {
					return interrupted
				}
				return nil
			}
			s, e := OpenDeployment(context.Background(), root, o)
			if s != nil {
				s.Close()
			}
			if !errors.Is(e, interrupted) {
				t.Fatal(e)
			}
			s, e = OpenDeployment(context.Background(), root, deploymentOptions())
			if e != nil {
				t.Fatal(e)
			}
			if got := readTest(t, s, "asset"); got != body {
				t.Fatal("raw changed")
			}
			m, e := s.ReadAssetMeta(context.Background(), "asset", 0)
			if e != nil || m.Revision != 4 {
				t.Fatal(m, e)
			}
			if lock, e := LockDirectory(filepath.Join(root, "assets", ".ownward.lock")); e == nil {
				lock.Close()
				t.Fatal("second opener acquired lock while active store held lock")
			}
			if e = s.Close(); e != nil {
				t.Fatal(e)
			}
			assertLegacyManifestFormat(t, root, retiredFormat)
			if _, e = os.Stat(filepath.Join(root, "assets", "information.jsonl")); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("old raw retained", e)
			}
			s, e = OpenDeployment(context.Background(), root, deploymentOptions())
			if e != nil {
				t.Fatal(e)
			}
			if got := readTest(t, s, "asset"); got != body {
				t.Fatal("restart changed raw")
			}
			s.Close()
		})
	}
}

func TestDeploymentDoesNotInitializeOverUnownedTarget(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "stores", "orphan"), 0700)
	s, e := OpenDeployment(context.Background(), root, deploymentOptions())
	if s != nil {
		s.Close()
	}
	if e == nil {
		t.Fatal("orphan target was overwritten")
	}
}

func TestDeploymentKeepsControlAndDerivedWithoutInference(t *testing.T) {
	// Initialize real ControlState via an isolated temporary SQLite store so
	// principal digest and schema match production informationcontrol rules.
	seedStore := retrievalStore(t)
	seedAuth, err := seedStore.OpenControlAuthority(context.Background(), contract.ControlState{
		Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "composition", ActiveKernelGeneration: "kernel",
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := informationcontrol.New(seedAuth).InitializeOwner("owner")
	if err != nil {
		t.Fatal(err)
	}
	controlBefore := seedAuth.ReadControl()

	root := t.TempDir()
	legacyFixture(t, root)
	writeLegacyControlEnvelope(t, root, controlBefore)
	record := derived.Record{
		AssetID: "asset", AssetRevision: 4, GeneratedAt: time.Now().UTC(), Status: "ready", Provider: "external",
		InputsKnown: true, EmbeddingSpace: "space", Embedding: make([]float32, 512),
		Analysis: semantics.Analysis{Summary: "保留的原组织", Topics: []string{"主题"}},
	}
	record.Embedding[1] = 1
	writeLegacyDerivedGeneration(t, root, "gen-1", record)

	s, e := OpenDeployment(context.Background(), root, deploymentOptions())
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	_, r, e := s.OpenControl(context.Background(), "authority")
	if e != nil {
		t.Fatal(e)
	}
	var after contract.ControlState
	e = json.NewDecoder(r).Decode(&after)
	r.Close()
	if e != nil || after.Revision != controlBefore.Revision || after.InformationControl.SystemID != controlBefore.InformationControl.SystemID {
		t.Fatal("control changed", e)
	}
	sum := sha256.Sum256([]byte(token))
	if _, e = s.BeginAccess(context.Background(), hex.EncodeToString(sum[:]), contract.ReadPermission); e != nil {
		t.Fatal("credential lost", e)
	}
	v, e := s.CurrentOrganization(context.Background(), "legacy-migration", "asset")
	if e != nil {
		t.Fatal(e)
	}
	h, e := s.RecordHeader(context.Background(), v)
	if e != nil || h.Analysis.Summary != record.Analysis.Summary {
		t.Fatal("organization lost", e)
	}
	generation, space, e := s.Generation(context.Background())
	if e != nil || space != "space" {
		t.Fatal(generation, space, e)
	}
	hits, _, e := s.VectorSearch(context.Background(), generation, space, record.Embedding, nil, 10)
	if e != nil || len(hits) != 1 {
		t.Fatal(hits, e)
	}
}

func TestDeploymentCancelBeforeActivationAndRejectAfter(t *testing.T) {
	for _, phase := range []string{"intent", "isolated-marker", "validated", "pointer", "activated"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			legacyFixture(t, root)
			o := deploymentOptions()
			o.checkpoint = func(p string) error {
				if p == phase {
					return errors.New("interrupted")
				}
				return nil
			}
			s, e := OpenDeployment(context.Background(), root, o)
			if s != nil {
				s.Close()
			}
			if e == nil {
				t.Fatal("did not stop")
			}
			e = CancelDeployment(context.Background(), root)
			if phase == "activated" {
				if e == nil {
					t.Fatal("rolled back activated store")
				}
				s, e = OpenDeployment(context.Background(), root, deploymentOptions())
				if e != nil {
					t.Fatal(e)
				}
				s.Close()
				return
			}
			if !errors.Is(e, ErrMigrationCancelled) && e != nil {
				t.Fatal(e)
			}
			assertLegacyManifestFormat(t, root, "ownward.asset-log/v3")
			if _, err := os.Stat(filepath.Join(root, "assets", "information.jsonl")); err != nil {
				t.Fatalf("legacy asset log missing after rollback: %v", err)
			}
		})
	}
}

func TestDeploymentCancelKeepsUncertainTargetIsolated(t *testing.T) {
	for _, damage := range []string{"missing", "corrupt"} {
		t.Run(damage, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			body := legacyFixture(t, root)
			interrupted := errors.New("interrupted after activation")
			o := deploymentOptions()
			o.checkpoint = func(phase string) error {
				if phase == "activated" {
					return interrupted
				}
				return nil
			}
			if s, err := OpenDeployment(ctx, root, o); !errors.Is(err, interrupted) {
				if s != nil {
					s.Close()
				}
				t.Fatal(err)
			}
			var state migrationState
			if err := readSmallJSON(filepath.Join(root, "migration.json"), &state); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "stores", state.ID, "ownward.sqlite")
			if err := os.Rename(path, path+".preserved"); err != nil {
				t.Fatal(err)
			}
			if damage == "corrupt" {
				if err := os.WriteFile(path, []byte("invalid database"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := CancelDeployment(ctx, root); err == nil {
				t.Fatal("uncertain activation allowed rollback")
			}
			assertLegacyManifestFormat(t, root, retiredFormat)
			if _, err := os.Stat(filepath.Join(root, "storage.json")); err != nil {
				t.Fatal("lost recovery pointer", err)
			}
			if damage == "corrupt" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(path+".preserved", path); err != nil {
				t.Fatal(err)
			}
			s, err := OpenDeployment(ctx, root, deploymentOptions())
			if err != nil {
				t.Fatal("forward recovery", err)
			}
			defer s.Close()
			if got := readTest(t, s, "asset"); got != body {
				t.Fatal("forward recovery changed original")
			}
		})
	}
}

func TestDeploymentCancelResumesDurableRollback(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	legacyFixture(t, root)
	o := deploymentOptions()
	o.checkpoint = func(phase string) error {
		if phase == "pointer" {
			return errors.New("interrupted before activation")
		}
		return nil
	}
	if s, err := OpenDeployment(ctx, root, o); err == nil {
		s.Close()
		t.Fatal("did not stop before activation")
	}
	var state migrationState
	if err := readSmallJSON(filepath.Join(root, "migration.json"), &state); err != nil {
		t.Fatal(err)
	}
	state.Phase = "rollback"
	if err := writeDurableJSON(filepath.Join(root, "migration.json"), state); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "storage.json")); err != nil {
		t.Fatal(err)
	}
	if err := CancelDeployment(ctx, root); !errors.Is(err, ErrMigrationCancelled) && err != nil {
		t.Fatal(err)
	}
	assertLegacyManifestFormat(t, root, "ownward.asset-log/v3")
	if _, err := os.Stat(filepath.Join(root, "assets", "information.jsonl")); err != nil {
		t.Fatalf("legacy asset log missing after resumed rollback: %v", err)
	}
}
