package informationcontrol_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/semantics"
)

var initial = contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "kernel"}

func setup(t *testing.T) (*authoritysubstrate.Substrate, *informationcontrol.Control, context.Context, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "store")
	a, err := authoritysubstrate.Open(root, initial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	c := informationcontrol.New(a.Control())
	token, err := c.InitializeOwner("所有者")
	if err != nil {
		t.Fatal(err)
	}
	return a, c, informationcontrol.Authenticate(context.Background(), token), root
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
	a, c, owner, _ := setup(t)
	s, err := core.NewWithAuthority(a.Assets())
	if err != nil {
		t.Fatal(err)
	}
	p := informationcontrol.NewProduct(s, c)
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
	a, c, owner, root := setup(t)
	d, err := derived.Open(filepath.Join(root, "derived"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := core.NewCollaborativeWithAuthority(a.Assets(), d, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := informationcontrol.NewProduct(s, c)
	defer p.Close()
	secret := "private-marker-f4b3a980"
	first, err := p.Create(owner, contract.CreateInput{Content: secret})
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Create(owner, contract.CreateInput{Content: "依赖资料"})
	if err != nil {
		t.Fatal(err)
	}
	third, err := p.Create(owner, contract.CreateInput{Content: "完全独立的菜谱"})
	if err != nil {
		t.Fatal(err)
	}
	dependent := derived.Record{AssetID: second.Information.ID, AssetRevision: 1, Status: "ready", Analysis: semantics.Analysis{Summary: secret}, InputAssets: []semantics.CandidateReference{{ID: first.Information.ID, Revision: 1}}}
	independent := derived.Record{AssetID: third.Information.ID, AssetRevision: 1, Status: "ready", Analysis: semantics.Analysis{Summary: "独立资料已整理"}, GeneratedAt: time.Now().UTC(), InputsKnown: true}
	if err := d.Put(dependent); err != nil {
		t.Fatal(err)
	}
	if err := d.Put(independent); err != nil {
		t.Fatal(err)
	}
	before, _ := d.GetWithEmbedding(third.Information.ID)
	// 模拟崩溃留下的受控临时副本和已关闭旧世代。
	if err := os.WriteFile(filepath.Join(d.Root(), ".organization-compacting-fixture"), []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	generation, err := derived.NewGenerationID(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	old, err := derived.CreateGeneration(d.Root(), generation)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.StageGeneration([]derived.Record{dependent}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
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
	after, _ := d.GetWithEmbedding(third.Information.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("无关资料组织质量被改动")
	}
	if _, err := p.Read(owner, second.Information.ID); err != nil {
		t.Fatal("依赖失效误删其他原文", err)
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
	if _, err := a.Assets().CreateAsset(domain.Information{Schema: domain.AssetSchema, ID: first.Information.ID, Revision: 1, Kind: domain.KindGeneral, Content: "recreated", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err == nil {
		t.Fatal("遗忘标识被重用")
	}
}

func TestRestoreKeepsOwnershipButInvalidatesOldCredentials(t *testing.T) {
	a, c, owner, root := setup(t)
	before := a.Control().ReadControl().InformationControl.SystemID
	backup := filepath.Join(t.TempDir(), "backup.zip")
	if err := a.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InitializeOwner("another"); err == nil {
		t.Fatal("可重新认领所有权")
	}
	target := filepath.Join(filepath.Dir(root), "restored")
	if err := authoritysubstrate.Restore(backup, target, initial); err != nil {
		t.Fatal(err)
	}
	restored, err := authoritysubstrate.Open(target, initial)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	recovered := informationcontrol.New(restored.Control())
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
	if restored.Control().ReadControl().InformationControl.SystemID != before {
		t.Fatal("恢复改变信息体系身份")
	}
}
