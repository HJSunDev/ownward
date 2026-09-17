package boundedstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
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
			// This is the real legacy opener, not a mocked marker interpretation.
			old, e := assetlog.Open(filepath.Join(root, "assets"))
			if e == nil {
				old.Close()
				t.Fatal("old binary opened while new store held lock")
			}
			if e = s.Close(); e != nil {
				t.Fatal(e)
			}
			old, e = assetlog.Open(filepath.Join(root, "assets"))
			if e == nil {
				old.Close()
				t.Fatal("old binary ignored retired marker")
			}
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
	root := t.TempDir()
	legacyFixture(t, root)
	a, e := authoritysubstrate.Open(root, contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "composition", ActiveKernelGeneration: "kernel"})
	if e != nil {
		t.Fatal(e)
	}
	c := informationcontrol.New(a.Control())
	token, e := c.InitializeOwner("owner")
	if e != nil {
		t.Fatal(e)
	}
	controlBefore := a.Control().ReadControl()
	a.Close()
	d, e := derived.Open(filepath.Join(root, "state"))
	if e != nil {
		t.Fatal(e)
	}
	record := derived.Record{AssetID: "asset", AssetRevision: 4, GeneratedAt: time.Now().UTC(), Status: "ready", Provider: "external", InputsKnown: true, EmbeddingSpace: "space", Embedding: make([]float32, 512), Analysis: semantics.Analysis{Summary: "保留的原组织", Topics: []string{"主题"}}}
	record.Embedding[1] = 1
	if e = d.Put(record); e != nil {
		t.Fatal(e)
	}
	d.Close()
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
			if e != nil {
				t.Fatal(e)
			}
			old, e := assetlog.Open(filepath.Join(root, "assets"))
			if e != nil {
				t.Fatal(e)
			}
			old.Close()
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
			if old, err := assetlog.Open(filepath.Join(root, "assets")); err == nil {
				old.Close()
				t.Fatal("legacy authority reopened")
			}
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
	if err := CancelDeployment(ctx, root); err != nil {
		t.Fatal(err)
	}
	old, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
}
