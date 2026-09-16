package boundedstore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
)

func TestRevocationBlocksPreparedMutationAndDelivery(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "ownward.sqlite"))
	ctx := context.Background()
	p := contract.Principal{ID: "principal", Revision: 1, CredentialDigest: strings.Repeat("a", 64), Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission}}
	if err := s.PublishAccess(ctx, AccessHeader{System: "system", Revision: 1}, 0, []contract.Principal{p}); err != nil {
		t.Fatal(err)
	}
	writeCtx, err := s.BeginAccess(ctx, p.CredentialDigest, contract.MaintainPermission)
	if err != nil {
		t.Fatal(err)
	}
	op := operation("revoke")
	write := stageTest(t, s, op, "a", "正文", 1)
	p.Revision = 2
	p.Permissions = nil
	if err = s.PublishAccess(ctx, AccessHeader{System: "system", Revision: 2}, 1, []contract.Principal{p}); err != nil {
		t.Fatal(err)
	}
	if err = s.Publish(writeCtx, receipt(op, write), []AssetWrite{write}); !errors.Is(err, ErrAccess) {
		t.Fatal("已撤销写入未拒绝", err)
	}
	if err = s.AuthorizeDelivery(writeCtx, nil); !errors.Is(err, ErrAccess) {
		t.Fatal("已撤销交付未拒绝", err)
	}
	p.Revision = 3
	p.Permissions = []contract.Permission{contract.ReadPermission, contract.MaintainPermission}
	if err = s.PublishAccess(ctx, AccessHeader{System: "system", Revision: 3}, 2, []contract.Principal{p}); err != nil {
		t.Fatal(err)
	}
	if err = s.AuthorizeDelivery(writeCtx, nil); !errors.Is(err, ErrAccess) {
		t.Fatal("重新授权复活了旧调用", err)
	}
	if _, err = s.ReadAssetMeta(ctx, "a", 0); !errors.Is(err, ErrNotFound) {
		t.Fatal("被拒绝暂存可见", err)
	}
	if err = s.Abandon(ctx, write.Payload); err != nil {
		t.Fatal(err)
	}
}

func TestControlCASAndStorageLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ownward.sqlite")
	s := openTest(t, path)
	ctx := context.Background()
	if other, err := Open(ctx, path, Options{Budget: s.budget}); err == nil {
		other.Close()
		t.Fatal("重复实例取得资料库")
	}
	p, err := s.Stage(ctx, "control-one", StringSource("控制决定"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PublishControl(ctx, ControlRecord{Key: "decision", Revision: 1, Payload: p}, 0); err != nil {
		t.Fatal(err)
	}
	p2, err := s.Stage(ctx, "control-two", StringSource("另一决定"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PublishControl(ctx, ControlRecord{Key: "decision", Revision: 1, Payload: p2}, 0); err == nil {
		t.Fatal("过期控制修订被接受")
	}
	revision, r, err := s.OpenControl(ctx, "decision")
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if revision != 1 {
		t.Fatal("控制修订变化")
	}
}
