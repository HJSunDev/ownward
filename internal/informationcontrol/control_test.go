package informationcontrol_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
)

var initial = contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "kernel"}

type controlTestEnv struct {
	store  *boundedstore.Store
	kernel *core.StreamingAssets
	budget *resourcebudget.Budget
}

func openControlEnv(t *testing.T, root string, withEmbedder bool) (*controlTestEnv, *informationcontrol.Control) {
	t.Helper()
	ctx := context.Background()
	scratch := filepath.Join(root, "scratch")
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		t.Fatal(err)
	}
	budget, err := resourcebudget.New(20*resourcebudget.MiB, 4*resourcebudget.MiB)
	if err != nil {
		t.Fatal(err)
	}
	var embedder contract.VectorCapability
	space := ""
	if withEmbedder {
		embedder = embedding.HashForTesting{Dimensions: 512}
		space = embedder.Space().ID
	}
	store, err := boundedstore.OpenDeployment(ctx, root, boundedstore.DeploymentOptions{
		Options:     boundedstore.Options{Budget: budget},
		VectorSpace: space,
		Initialize: func(ctx context.Context, s *boundedstore.Store, _ string) error {
			if _, err := s.OpenControlAuthority(ctx, initial); err != nil {
				return err
			}
			if space != "" {
				return s.InitializeDeploymentSpace(ctx, space)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.OpenControlAuthority(ctx, initial)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	kernel := &core.StreamingAssets{Store: store, Budget: budget, Scratch: scratch, DiskBytes: 256 * resourcebudget.MiB, Embedder: embedder}
	env := &controlTestEnv{store: store, kernel: kernel, budget: budget}
	t.Cleanup(func() {
		_ = kernel.Close()
		_ = store.Close()
	})
	return env, informationcontrol.New(a)
}

func setup(t *testing.T) (*controlTestEnv, *informationcontrol.Control, context.Context, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "store")
	env, c := openControlEnv(t, root, true)
	token, err := c.InitializeOwner("所有者")
	if err != nil {
		t.Fatal(err)
	}
	return env, c, informationcontrol.Authenticate(context.Background(), token), root
}

func TestRevocationBlocksInflightCommitAndDeliveryEvenAfterRegrant(t *testing.T) {
	_, c, owner, _ := setup(t)
	reader, token, err := c.Enroll(owner, "日常智能体")
	if err != nil {
		t.Fatal(err)
	}
	ctx := informationcontrol.Authenticate(context.Background(), token)
	if err := c.SetPermissions(owner, reader.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission}); err != nil {
		t.Fatal(err)
	}
	bound, finish, err := c.Begin(ctx, contract.MaintainPermission)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetPermissions(ctx, reader.ID, []contract.Permission{contract.ManagePermission}); err == nil {
		t.Fatal("普通主体自行提权")
	}
	if err := c.SetPermissions(owner, reader.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.SetPermissions(owner, reader.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission}); err != nil {
		t.Fatal(err)
	}
	wrote := false
	if err := contract.Commit(bound, func() error { wrote = true; return nil }); err == nil || wrote {
		t.Fatal("旧请求在撤销后提交")
	}
	if err := finish(); err == nil {
		t.Fatal("旧请求在撤销后交付")
	}
	if _, _, err := c.Begin(context.Background(), contract.ReadPermission); err == nil {
		t.Fatal("匿名读取")
	}
}

func TestApprovalIsOperationBoundAndDoesNotGrantManagement(t *testing.T) {
	env, c, owner, _ := setup(t)
	p := informationcontrol.NewProduct(env.kernel, c)
	defer p.Close()
	reader, token, err := c.Enroll(owner, "申请者")
	if err != nil {
		t.Fatal(err)
	}
	ctx := informationcontrol.Authenticate(context.Background(), token)
	r := contract.ManagementRequest{ID: "grant-1", Operation: "permissions", SubjectID: reader.ID, Permissions: []contract.Permission{contract.ReadPermission}}
	op, err := p.Manage(ctx, r)
	if err != nil || op.Status != "awaiting_approval" {
		t.Fatalf("proposal %v %v", op, err)
	}
	if _, err := p.Decide(ctx, r.ID, true); err == nil {
		t.Fatal("申请者批准自己")
	}
	op, err = p.Decide(owner, r.ID, true)
	if err != nil || op.Status != "completed" {
		t.Fatalf("approved %v %v", op, err)
	}
	if _, err := p.Principals(ctx); err == nil {
		t.Fatal("单次批准变成管理权")
	}
	r.Permissions = []contract.Permission{contract.ManagePermission}
	if _, err := p.Manage(ctx, r); err == nil {
		t.Fatal("操作标识可替换内容")
	}
	r.ID = "grant-2"
	if _, err := p.Manage(ctx, r); err != nil {
		t.Fatal(err)
	}
	op, err = p.Decide(owner, r.ID, false)
	if err != nil || op.Status != "declined" {
		t.Fatalf("decline %v %v", op, err)
	}
}

func TestForgetCleansCopiesWithoutModelAndPreservesUnrelatedQuality(t *testing.T) {
	env, c, owner, root := setup(t)
	p := informationcontrol.NewProduct(env.kernel, c)
	defer p.Close()
	secret := "private-marker-f4b3a980"
	first, err := p.Create(owner, contract.CreateInput{Content: secret})
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Create(owner, contract.CreateInput{Content: "依赖资料", Relations: []domain.ExplicitRelation{{Type: "related_to", TargetID: first.Information.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	third, err := p.Create(owner, contract.CreateInput{Content: "完全独立的菜谱"})
	if err != nil {
		t.Fatal(err)
	}
	works, err := p.SemanticWorkFor(owner, []string{second.Information.ID, third.Information.ID})
	if err != nil || len(works) != 2 {
		t.Fatalf("SemanticWorkFor: %v %#v", err, works)
	}
	byAsset := map[string]semantics.Work{works[0].Asset.ID: works[0], works[1].Asset.ID: works[1]}
	wSecond := byAsset[second.Information.ID]
	if _, err := p.SubmitSemantic(owner, semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: wSecond.ID, AssetID: second.Information.ID, Revision: 1,
		Capability: semantics.Capability{ID: "fixture", Version: "v1"}, Status: semantics.SubmissionComplete,
		Analysis: semantics.Analysis{
			Summary: secret,
			Organization: &semantics.Organization{
				Schema: semantics.OrganizationSchema,
				Units:  []semantics.SemanticUnit{{ID: "u", Statement: secret}},
				Links: []semantics.GroundedLink{{
					Type: "related_to", Meaning: secret,
					Source: semantics.GraphEndpoint{AssetID: second.Information.ID, UnitID: "u"},
					Target: semantics.GraphEndpoint{AssetID: first.Information.ID},
				}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	wThird := byAsset[third.Information.ID]
	if _, err := p.SubmitSemantic(owner, semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: wThird.ID, AssetID: third.Information.ID, Revision: 1,
		Capability: semantics.Capability{ID: "fixture", Version: "v1"}, Status: semantics.SubmissionComplete,
		Analysis: semantics.Analysis{Summary: "独立资料已整理"},
	}); err != nil {
		t.Fatal(err)
	}
	// 模拟尚未交付完成的流式读取副本，遗忘清理后须自动失效且释放底层暂存。
	undelivered, err := env.kernel.ExecuteStream(owner, contract.StreamRequest{
		Operation: "ownward_read",
		Arguments: boundedstore.StringSource(`{"id":"` + first.Information.ID + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	front := time.Now()
	op, err := p.Manage(owner, contract.ManagementRequest{ID: "forget-1", Operation: "forget", Targets: []contract.AssetVersion{{ID: first.Information.ID, Revision: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != "cleaning" && op.Status != "completed" {
		t.Fatalf("unexpected state %s", op.Status)
	}
	frontLatency := time.Since(front)
	if _, err := p.Read(owner, first.Information.ID); err == nil {
		t.Fatal("遗忘后仍可读取")
	}
	deadline := time.After(5 * time.Second)
	for op.Status != "completed" {
		select {
		case <-deadline:
			t.Fatalf("未完成 %v", op)
		case <-time.After(10 * time.Millisecond):
		}
		op, err = p.Receipt(owner, "forget-1")
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("forget foreground=%s, cleanup completion=%s, semantic/model rebuild calls=0", frontLatency, time.Since(front))

	if _, err := undelivered.Retain(func() error { return nil }); err == nil {
		t.Fatal("未交付流式副本在遗忘后仍可继续保留交付")
	}
	_ = undelivered.Close()
	thirdHits, err := p.Search(owner, contract.SearchInput{Query: "菜谱", Limit: 5})
	if err != nil || len(thirdHits) != 1 || thirdHits[0].Summary != "独立资料已整理" {
		t.Fatalf("无关资料组织质量被改动: %#v %v", thirdHits, err)
	}
	if _, err := p.Read(owner, second.Information.ID); err != nil {
		t.Fatal("依赖失效误删其他原文", err)
	}
	secondHits, err := p.Search(owner, contract.SearchInput{Query: "依赖资料", Limit: 5})
	if err != nil || len(secondHits) != 1 || strings.Contains(secondHits[0].Summary, secret) {
		t.Fatalf("受污染派生摘要未清除: %#v %v", secondHits, err)
	}
	// Close store so WAL checkpoints cleanly, then verify zero secret bytes remain on disk.
	p.Close()
	_ = env.kernel.Close()
	if err := env.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || strings.HasSuffix(path, ".lock") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), secret) {
			t.Errorf("受控副本残留于 %s", filepath.Base(path))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreKeepsOwnershipButInvalidatesOldCredentials(t *testing.T) {
	env, c, owner, root := setup(t)
	before := c.SystemID()
	backup := filepath.Join(t.TempDir(), "backup.zip")
	if err := env.store.ExportArchive(owner, backup); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InitializeOwner("another"); err == nil {
		t.Fatal("可重新认领所有权")
	}
	target := filepath.Join(filepath.Dir(root), "restored")
	if _, err := boundedstore.RestoreArchive(context.Background(), backup, target, boundedstore.Options{Budget: env.budget}); err != nil {
		t.Fatal(err)
	}
	_, recovered := openControlEnv(t, target, false)
	if _, _, err := recovered.Begin(owner, contract.ReadPermission); err == nil {
		t.Fatal("旧备份激活旧凭据")
	}
	token, err := recovered.RecoverOwner()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := recovered.Begin(informationcontrol.Authenticate(context.Background(), token), contract.ManagePermission); err != nil {
		t.Fatal(err)
	}
	if recovered.SystemID() != before {
		t.Fatal("恢复改变信息体系身份")
	}
}

func TestSharedGraphSurvivesAgentRevocationAndStopsAtForget(t *testing.T) {
	env, c, owner, _ := setup(t)
	p := informationcontrol.NewProduct(env.kernel, c)
	defer p.Close()
	principal, token, err := c.Enroll(owner, "temporary agent")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.SetPermissions(owner, principal.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission}); err != nil {
		t.Fatal(err)
	}
	agent := informationcontrol.Authenticate(context.Background(), token)
	first, err := p.Create(owner, contract.CreateInput{Content: "Lumen uses Beacon."})
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Create(agent, contract.CreateInput{Content: "Beacon is allowed only indoors."})
	if err != nil {
		t.Fatal(err)
	}
	w, err := p.SemanticWorkFor(agent, []string{second.Information.ID})
	if err != nil || len(w) != 1 {
		t.Fatal(err)
	}
	input := semantics.Submission{Schema: semantics.SubmissionSchema, WorkID: w[0].ID, AssetID: w[0].Asset.ID, Revision: w[0].Asset.Revision, Capability: semantics.Capability{ID: "fixture", Version: "v1"}, Status: semantics.SubmissionComplete, Analysis: semantics.Analysis{Summary: "Beacon needs indoor conditions", Organization: &semantics.Organization{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{{ID: "condition"}}, Links: []semantics.GroundedLink{{Type: "applies_in", Meaning: "Beacon's indoor restriction applies to its use by Lumen", Source: semantics.GraphEndpoint{AssetID: first.Information.ID}, Target: semantics.GraphEndpoint{AssetID: second.Information.ID, UnitID: "condition"}}}}}}
	if _, err = p.SubmitSemantic(agent, input); err != nil {
		t.Fatal(err)
	}
	if err = c.SetPermissions(owner, principal.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Navigate(agent, []string{first.Information.ID}, nil, 1, 10); err == nil {
		t.Fatal("revoked principal read graph")
	}
	if _, err = p.SubmitSemantic(agent, input); err == nil {
		t.Fatal("revoked principal submitted")
	}
	nav, err := p.Navigate(owner, []string{first.Information.ID}, nil, 1, 10)
	if err != nil || len(nav.Edges) != 1 {
		t.Fatalf("revocation damaged shared graph: %#v %v", nav, err)
	}
	if _, err = p.Manage(owner, contract.ManagementRequest{ID: "forget-graph", Operation: "forget", Targets: []contract.AssetVersion{{ID: first.Information.ID, Revision: 1}}}); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 10; attempt++ {
		nav, err = p.Navigate(owner, []string{second.Information.ID}, nil, 1, 10)
		if err == nil || !strings.Contains(err.Error(), "检索资料已变化") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || len(nav.Edges) != 0 {
		t.Fatalf("forgotten graph endpoint remained: %#v %v", nav, err)
	}
	if _, err = p.Read(owner, second.Information.ID); err != nil {
		t.Fatal("dependent original was deleted", err)
	}
	if _, err = p.SubmitSemantic(owner, input); err == nil {
		t.Fatal("forgotten input resurrected by late work")
	}
}
