package core

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
)

func TestClarificationsFollowFirstFragmentAndChanges(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assets")
	store, err := assetlog.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestBasic(t, store)
	ctx := contract.WithInformationSystem(context.Background(), "user-system")
	text := "原始记录：我住上海。\n" + strings.Repeat("其他资料与此无关。", 160) + "\n澄清：上海是记录错误，我一直住北京。"
	selector := &domain.TextSelector{Exact: "澄清：上海是记录错误，我一直住北京。"}
	created, err := s.Create(ctx, CreateInput{Content: text, Relations: []domain.ExplicitRelation{{Type: "qualifies", TargetID: "$self", Selector: selector}}})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Information.ID
	units := derived.BuildEvidenceUnits(created.Information)
	first, err := s.ReadEvidenceWithBasis(ctx, units[0].ID)
	if err != nil || len(first.Clarifications) != 1 || first.Clarifications[0].Covered || strings.Contains(first.Evidence.Content, "一直住北京") {
		t.Fatalf("首片段漏交说明入口: %+v %v", first, err)
	}
	note, err := s.ReadEvidenceWithBasis(ctx, first.Clarifications[0].Evidence.ID)
	if err != nil || note.Evidence.Content != selector.Exact || !note.Clarifications[0].Covered {
		t.Fatalf("说明未准确交付或自指重复: %+v %v", note, err)
	}
	full, err := s.ReadInformation(ctx, id)
	if err != nil || !full.Clarifications[0].Covered {
		t.Fatal(full, err)
	}
	assertStatus := func(ref, want string) {
		t.Helper()
		checks, err := s.CheckInformation(ctx, []string{ref})
		if err != nil || len(checks) != 1 || checks[0].Status != want {
			t.Fatalf("want %s: %+v %v", want, checks, err)
		}
	}
	assertStatus(first.Basis, "unchanged")
	invalid := strings.Replace(text, selector.Exact, "另一个说明", 1)
	if _, err := s.Update(ctx, UpdateInput{ID: id, ExpectedRevision: 1, Content: &invalid}); err == nil {
		t.Fatal("失配定位被提交")
	}
	ambiguous := text + selector.Exact
	if _, err := s.Update(ctx, UpdateInput{ID: id, ExpectedRevision: 1, Content: &ambiguous}); err == nil {
		t.Fatal("歧义定位被提交")
	}
	moved := "前置资料。" + text
	if _, err := s.Update(ctx, UpdateInput{ID: id, ExpectedRevision: 1, Content: &moved}); err != nil {
		t.Fatal(err)
	}
	assertStatus(first.Basis, "changed")
	current, _ := s.ReadInformation(ctx, id)
	separate, err := s.Create(ctx, CreateInput{Content: "今年迁居杭州，早年的居住记录保留。", Relations: []domain.ExplicitRelation{{Type: "qualifies", TargetID: id}}})
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(current.Basis, "changed")
	current, _ = s.ReadInformation(ctx, id)
	if len(current.Clarifications) != 2 {
		t.Fatal("独立说明未随来源交付")
	}
	changed := "计划下月迁居杭州，尚未发生。"
	if _, err := s.Update(ctx, UpdateInput{ID: separate.Information.ID, ExpectedRevision: 1, Content: &changed}); err != nil {
		t.Fatal(err)
	}
	assertStatus(current.Basis, "changed")
	current, _ = s.ReadInformation(ctx, id)
	backup := filepath.Join(t.TempDir(), "backup.zip")
	if err := store.Compact(); err != nil {
		t.Fatal(err)
	}
	if err := store.Backup(backup); err != nil {
		t.Fatal(err)
	}
	restore := filepath.Join(t.TempDir(), "restored")
	if err := assetlog.Restore(backup, restore); err != nil {
		t.Fatal(err)
	}
	restored, err := assetlog.Open(restore)
	if err != nil {
		t.Fatal(err)
	}
	rs := newTestBasic(t, restored)
	defer rs.Close()
	checks, err := rs.CheckInformation(ctx, []string{current.Basis})
	if err != nil || checks[0].Status != "unchanged" {
		t.Fatal("迁移或压实改变依据", checks, err)
	}
	if err := store.Delete([]assetlog.Deletion{{ID: separate.Information.ID, Revision: 2}}); err != nil {
		t.Fatal(err)
	}
	assertStatus(current.Basis, "changed")
	if err := store.Delete([]assetlog.Deletion{{ID: id, Revision: 2}}); err != nil {
		t.Fatal(err)
	}
	assertStatus(current.Basis, "unavailable")
	_ = s.Close()
	if _, _, err := store.ReadSource(id); err == nil {
		t.Fatal("关闭后仍宣称可读取")
	}
}

func TestBasisDetectsRestoredRevisionBranchAndBounds(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assets")
	store, err := assetlog.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestBasic(t, store)
	defer s.Close()
	ctx := contract.WithInformationSystem(context.Background(), "same-system")
	c, err := s.Create(ctx, CreateInput{Content: strings.Repeat("共同片段", 200) + "甲"})
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "base.zip")
	if err := store.Backup(archive); err != nil {
		t.Fatal(err)
	}
	branch := strings.Repeat("共同片段", 200) + "乙"
	u, err := s.Update(ctx, UpdateInput{ID: c.Information.ID, ExpectedRevision: 1, Content: &branch})
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.ReadEvidenceWithBasis(ctx, derived.BuildEvidenceUnits(u.Information)[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored")
	if err := assetlog.Restore(archive, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := assetlog.Open(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	rs := newTestBasic(t, restored)
	defer rs.Close()
	branch = strings.Repeat("共同片段", 200) + "丙"
	if _, err := rs.Update(ctx, UpdateInput{ID: c.Information.ID, ExpectedRevision: 1, Content: &branch}); err != nil {
		t.Fatal(err)
	}
	checks, err := rs.CheckInformation(ctx, []string{read.Basis, "", "b1-e30"})
	if err != nil || checks[0].Status != "changed" || checks[1].Status != "unverifiable" || checks[2].Status != "unverifiable" {
		t.Fatal(checks, err)
	}
	checks, err = s.CheckInformation(contract.WithInformationSystem(ctx, "other-system"), []string{read.Basis})
	if err != nil || checks[0].Status != "unverifiable" {
		t.Fatal(checks, err)
	}
	if _, err = s.CheckInformation(ctx, make([]string, 65)); err == nil {
		t.Fatal("未限制传输批量")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.CheckInformation(canceled, []string{read.Basis}); err == nil {
		t.Fatal("忽略取消")
	}
}

func TestSourceSnapshotDoesNotMixConcurrentRevisions(t *testing.T) {
	store, err := assetlog.Open(filepath.Join(t.TempDir(), "assets"))
	if err != nil {
		t.Fatal(err)
	}
	s := newTestBasic(t, store)
	defer s.Close()
	ctx := contract.WithInformationSystem(context.Background(), "system")
	c, err := s.Create(ctx, CreateInput{Content: "version 1", Relations: []domain.ExplicitRelation{{Type: "qualifies", TargetID: "$self", Selector: &domain.TextSelector{Exact: "version 1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for rev := uint64(1); rev < 31; rev++ {
			text := fmt.Sprintf("version %d", rev+1)
			relations := []domain.ExplicitRelation{{Type: "qualifies", TargetID: c.Information.ID, Selector: &domain.TextSelector{Exact: text}}}
			if _, err := s.Update(ctx, UpdateInput{ID: c.Information.ID, ExpectedRevision: rev, Content: &text, Relations: &relations}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 100; i++ {
		r, err := s.ReadInformation(ctx, c.Information.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Information.Content != fmt.Sprintf("version %d", r.Information.Revision) || len(r.Clarifications) != 1 || r.Clarifications[0].SourceRevision != r.Information.Revision || !r.Clarifications[0].Covered {
			t.Fatal("混合快照", r)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
