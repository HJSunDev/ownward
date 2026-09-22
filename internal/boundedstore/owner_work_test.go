package boundedstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func ownerFixture(t *testing.T) (*Store, *informationcontrol.Control, context.Context) {
	t.Helper()
	s := openOwnerTest(t, filepath.Join(t.TempDir(), "owner.sqlite"))
	a, e := s.OpenControlAuthority(context.Background(), ownerInitial())
	if e != nil {
		t.Fatal(e)
	}
	c := informationcontrol.New(a)
	token, e := c.InitializeOwner("Owner")
	if e != nil {
		t.Fatal(e)
	}
	return s, c, informationcontrol.Authenticate(context.Background(), token)
}
func openOwnerTest(t *testing.T, path string) *Store {
	t.Helper()
	s, e := Open(context.Background(), path, deploymentOptions().Options)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	})
	return s
}
func ownerInitial() contract.ControlState {
	return contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "composition", ActiveKernelGeneration: "kernel"}
}
func workAgent(t *testing.T, c *informationcontrol.Control, owner context.Context) (contract.Principal, context.Context) {
	t.Helper()
	p, token, e := c.Enroll(owner, "Writer")
	if e != nil {
		t.Fatal(e)
	}
	if e = c.SetPermissions(owner, p.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission}); e != nil {
		t.Fatal(e)
	}
	return p, informationcontrol.Authenticate(context.Background(), token)
}
func draftText(t *testing.T, s *Store, ctx context.Context, id, grant string) string {
	t.Helper()
	_, r, e := s.ReadDraft(ctx, id, grant)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	b, e := io.ReadAll(r)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func newDraft(t *testing.T, s *Store, ctx context.Context, text string) contract.Draft {
	t.Helper()
	d, e := s.CreateDraft(ctx, contract.DraftInput{Content: StringSource(text)})
	if e != nil {
		t.Fatal(e)
	}
	return d
}

func TestOwnerDraftPrivacyDurabilityAndPublication(t *testing.T) {
	s, c, ctx := ownerFixture(t)
	p, agent := workAgent(t, c, ctx)
	d := newDraft(t, s, ctx, "")
	if _, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "blank"}); e == nil {
		t.Fatal("blank published")
	}
	d, e := s.WriteDraft(ctx, contract.DraftWrite{ID: d.ID, ExpectedRevision: d.Revision, Content: StringSource("私有🙂 uniquedrafttoken")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ListDrafts(agent, "", 10); !errors.Is(e, ErrAccess) {
		t.Fatal("agent enumerated drafts", e)
	}
	if _, e = s.OwnerEvents(agent, 0, 10); !errors.Is(e, ErrAccess) {
		t.Fatal("agent read owner history", e)
	}
	if _, r, e := s.ReadDraft(agent, d.ID, ""); e == nil {
		r.Close()
		t.Fatal("ungranted read")
	}
	for _, table := range []string{"assets", "semantic_jobs", "lexical_documents", "organizations"} {
		if n := countTest(t, s, "SELECT count(*) FROM "+table); n != 0 {
			t.Fatal("private draft leaked", table, n)
		}
	}
	page, e := s.ScanAssets(agent, "", 4096)
	if e != nil || len(page.Items) != 0 {
		t.Fatal("asset scan leaked", page, e)
	}
	hits, e := s.LexicalSearch(agent, "uniquedrafttoken", nil, 10)
	if e != nil || len(hits) != 0 {
		t.Fatal("search leaked", e)
	}
	g, e := s.GrantDraft(ctx, d.ID, p.ID, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if got := draftText(t, s, agent, d.ID, g.ID); got != "私有🙂 uniquedrafttoken" {
		t.Fatal(got)
	}
	d, e = s.WriteDraft(agent, contract.DraftWrite{ID: d.ID, GrantID: g.ID, ExpectedRevision: d.Revision, Append: true, Content: StringSource(" appended")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PublishDraft(agent, d.ID, d.Revision, contract.OperationIdentity{ID: "agent-publish"}); !errors.Is(e, ErrAccess) {
		t.Fatal("agent published", e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s = openOwnerTest(t, s.path)
	if got := draftText(t, s, ctx, d.ID, ""); got != "私有🙂 uniquedrafttoken appended" {
		t.Fatal(got)
	}
	a, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "publish"})
	if e != nil {
		t.Fatal(e)
	}
	if got := readTest(t, s, a.ID); got != "私有🙂 uniquedrafttoken appended" {
		t.Fatal(got)
	}
	again, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "publish"})
	if e != nil || again != a {
		t.Fatal("retry differed", again, e)
	}
	if countTest(t, s, "SELECT count(*) FROM owner_drafts") != 0 || countTest(t, s, "SELECT count(*) FROM owner_draft_grants") != 0 {
		t.Fatal("published work survived")
	}
	events, e := s.OwnerEvents(ctx, 0, 100)
	if e != nil || len(events.Items) != 2 {
		t.Fatal("publication events", events, e)
	}
	if events.Items[0].Kind != "created" || events.Items[1].Kind != "draft_published" {
		t.Fatal(events)
	}
	if countTest(t, s, "SELECT count(*) FROM semantic_jobs") != 1 {
		t.Fatal("publication did not schedule organization")
	}
}

func TestOwnerDraftRevocationCASAndReaderBounds(t *testing.T) {
	s, c, ctx := ownerFixture(t)
	p, agent := workAgent(t, c, ctx)
	d := newDraft(t, s, ctx, strings.Repeat("private🙂", ChunkBytes))
	g, e := s.GrantDraft(ctx, d.ID, p.ID, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	_, r, e := s.ReadDraft(agent, d.ID, g.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if _, e = r.Read(make([]byte, 1)); e != nil {
		t.Fatal(e)
	}
	if e = s.RevokeDraftGrant(ctx, g.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Read(make([]byte, 1)); !errors.Is(e, ErrAccess) {
		t.Fatal("buffer bypassed revocation", e)
	}
	g, e = s.GrantDraft(ctx, d.ID, p.ID, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.SetPermissions(ctx, p.ID, nil); e != nil {
		t.Fatal(e)
	}
	if _, r, e = s.ReadDraft(agent, d.ID, g.ID); !errors.Is(e, ErrAccess) {
		if r != nil {
			r.Close()
		}
		t.Fatal("principal revoke ignored", e)
	}
	changed, e := s.WriteDraft(ctx, contract.DraftWrite{ID: d.ID, ExpectedRevision: d.Revision, Content: StringSource("new content")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.WriteDraft(ctx, contract.DraftWrite{ID: d.ID, ExpectedRevision: d.Revision, Content: StringSource("stale overwrite")}); !errors.Is(e, ErrDraftConflict) {
		t.Fatal("stale write", e)
	}
	if got := draftText(t, s, ctx, d.ID, ""); got != "new content" {
		t.Fatal(got)
	}
	if e = s.DiscardDraft(ctx, d.ID, d.Revision); !errors.Is(e, ErrDraftConflict) {
		t.Fatal("stale discard", e)
	}
	if e = s.DiscardDraft(ctx, d.ID, changed.Revision); e != nil {
		t.Fatal(e)
	}
}

func TestOwnerDraftPublishAtomicFailureAndConcurrentRetry(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	stopMaintenance(s)
	d := newDraft(t, s, ctx, "atomic publish text")
	if _, e := s.writer.ExecContext(ctx, `CREATE TRIGGER fail_owner_publish BEFORE INSERT ON owner_events WHEN NEW.kind='draft_published' BEGIN SELECT RAISE(ABORT,'injected failure'); END`); e != nil {
		t.Fatal(e)
	}
	op := contract.OperationIdentity{ID: "atomic"}
	if _, e := s.PublishDraft(ctx, d.ID, d.Revision, op); e == nil {
		t.Fatal("injection did not fail")
	}
	for _, table := range []string{"assets", "owner_events", "operation_receipts", "semantic_jobs"} {
		if countTest(t, s, "SELECT count(*) FROM "+table) != 0 {
			t.Fatal("half commit", table)
		}
	}
	if got := draftText(t, s, ctx, d.ID, ""); got != "atomic publish text" {
		t.Fatal(got)
	}
	if _, e := s.writer.ExecContext(ctx, "DROP TRIGGER fail_owner_publish"); e != nil {
		t.Fatal(e)
	}
	const n = 6
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]contract.AssetVersion, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); <-start; results[i], errs[i] = s.PublishDraft(ctx, d.ID, d.Revision, op) }(i)
	}
	close(start)
	wg.Wait()
	for i := range results {
		if errs[i] != nil || results[i] != results[0] {
			t.Fatal("retry differed", results, errs)
		}
	}
	if countTest(t, s, "SELECT count(*) FROM assets") != 1 || countTest(t, s, "SELECT count(*) FROM owner_events") != 2 {
		t.Fatal("retry duplicated transaction")
	}
	drainTest(t, s)
}

func TestOwnerDraftSourceEvidenceConflictAndForget(t *testing.T) {
	s, c, ctx := ownerFixture(t)
	stopMaintenance(s)
	op := operation("external-source")
	v := stageTest(t, s, op, "source", "original evidence", 1)
	if e := s.Abandon(ctx, v.Payload); e != nil {
		t.Fatal(e)
	}
	p, e := s.Stage(ctx, operationKey(op), StringSource("original evidence"), StringSource(`{"source":{"actor":"publisher","ref":"https://example.test/document"},"contexts":[{"key":"topic","value":"facts"}]}`))
	if e != nil {
		t.Fatal(e)
	}
	v.Payload = p
	if e = s.Publish(ctx, receipt(op, v), []AssetWrite{v}); e != nil {
		t.Fatal(e)
	}
	d, e := s.CreateDraft(ctx, contract.DraftInput{Target: contract.AssetVersion{ID: "source", Revision: 1}, Content: StringSource("owner correction")})
	if e != nil {
		t.Fatal(e)
	}
	other, e := s.CreateDraft(ctx, contract.DraftInput{Target: d.Target})
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "revise"})
	if e != nil {
		t.Fatal(e)
	}
	if a.ID != "source" || a.Revision != 2 {
		t.Fatal(a)
	}
	if _, e = s.PublishDraft(ctx, other.ID, other.Revision, contract.OperationIdentity{ID: "stale-target"}); e == nil {
		t.Fatal("stale target overwritten")
	}
	if got := draftText(t, s, ctx, other.ID, ""); got != "original evidence" {
		t.Fatal(got)
	}
	drainTest(t, s)
	rev, r, e := s.OpenOriginal(ctx, a.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(r)
	r.Close()
	if e != nil || rev != 1 || string(b) != "original evidence" {
		t.Fatal(rev, string(b), e)
	}
	r, e = s.OpenDetails(ctx, a.ID, a.Revision)
	if e != nil {
		t.Fatal(e)
	}
	b, e = io.ReadAll(r)
	r.Close()
	if e != nil || !strings.Contains(string(b), ownerSourceActor) || strings.Contains(string(b), "example.test") {
		t.Fatal("wrong source attribution", string(b), e)
	}
	d, e = s.CreateDraft(ctx, contract.DraftInput{Target: a, Content: StringSource("second correction")})
	if e != nil {
		t.Fatal(e)
	}
	a, e = s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "revise-again"})
	if e != nil {
		t.Fatal(e)
	}
	pers := newDraft(t, s, ctx, "independent private draft")
	principal, agent := workAgent(t, c, ctx)
	g, e := s.GrantDraft(ctx, other.ID, principal.ID, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.StageForget(ctx, "forget-source", []ForgetTarget{{a.ID, a.Revision}}); e != nil {
		t.Fatal(e)
	}
	if e = s.CommitForget(ctx, "forget-source"); e != nil {
		t.Fatal(e)
	}
	if _, r, e = s.ReadDraft(agent, other.ID, g.ID); !errors.Is(e, ErrNotFound) {
		if r != nil {
			r.Close()
		}
		t.Fatal("forgotten draft visible", e)
	}
	if _, r, e = s.OpenOriginal(ctx, a.ID, false); !errors.Is(e, ErrNotFound) {
		if r != nil {
			r.Close()
		}
		t.Fatal("forgotten original visible", e)
	}
	drainTest(t, s)
	if countTest(t, s, "SELECT count(*) FROM asset_originals") != 0 || countTest(t, s, "SELECT count(*) FROM owner_draft_grants") != 0 {
		t.Fatal("forget left evidence or grants")
	}
	if countTest(t, s, "SELECT count(*) FROM payloads") != 1 {
		t.Fatal("forget left payloads")
	}
	if got := draftText(t, s, ctx, pers.ID, ""); got != "independent private draft" {
		t.Fatal(got)
	}
	// Forget removes content, not the bounded, content-free fact that an
	// operation happened. A retained reference cannot reopen the forgotten asset.
	events, e := s.OwnerEvents(ctx, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	retained := false
	for _, event := range events.Items {
		if event.Asset.ID == a.ID {
			retained = true
		}
	}
	if !retained {
		t.Fatal("bounded operation history lost")
	}
	if _, e = s.ReadAssetMeta(ctx, a.ID, 0); !errors.Is(e, ErrNotFound) {
		t.Fatal("history reopened forgotten asset", e)
	}
}

func TestOwnerDraftDiscardCleansProtectedSnapshot(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	d := newDraft(t, s, ctx, "discard-private-marker")
	before := countTest(t, s, "SELECT value FROM store_meta WHERE key='asset_epoch'")
	snapshot, e := s.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.DiscardDraft(ctx, d.ID, d.Revision); e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	if _, e = os.Stat(snapshot.Path); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("discarded snapshot survives", e)
	}
	if countTest(t, s, "SELECT count(*) FROM payloads") != 0 || countTest(t, s, "SELECT count(*) FROM owner_events") != 0 {
		t.Fatal("discard left payload or event")
	}
	if countTest(t, s, "SELECT value FROM store_meta WHERE key='asset_epoch'") != before {
		t.Fatal("private work invalidated asset cursors")
	}
}

func TestOwnerHistoryRetentionAndControlQueue(t *testing.T) {
	s, c, ctx := ownerFixture(t)
	stopMaintenance(s)
	p, agent := workAgent(t, c, ctx)
	request := contract.ManagementRequest{ID: "permission-decision", Operation: "permissions", SubjectID: p.ID, Permissions: []contract.Permission{contract.ReadPermission}}
	if _, e := c.Propose(agent, request); e != nil {
		t.Fatal(e)
	}
	items, _, e := s.OwnerOperations(ctx, "", 10, true)
	if e != nil || len(items) != 1 {
		t.Fatal(items, e)
	}
	if _, e = c.Propose(agent, request); e != nil {
		t.Fatal(e)
	}
	if countTest(t, s, "SELECT count(*) FROM owner_events") != 1 {
		t.Fatal("identical replay duplicated event")
	}
	// Preserve a pending decision even beyond the completed-history retention.
	if _, e = s.writer.ExecContext(ctx, "UPDATE owner_operation_times SET updated=0"); e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	items, _, e = s.OwnerOperations(ctx, "", 10, true)
	if e != nil || len(items) != 1 {
		t.Fatal("pending expired", e)
	}
	e = s.write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < OwnerEventLimit+10; i++ {
			if e := recordOwnerEvent(ctx, tx, "created", "asset", 1, "test", "completed"); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	if n := countTest(t, s, "SELECT count(*) FROM owner_events"); n != OwnerEventLimit {
		t.Fatal("event bound", n)
	}
	page, e := s.OwnerEvents(ctx, 1, 100)
	if e != nil || !page.Reset || len(page.Items) != 100 {
		t.Fatal(page, e)
	}
	if _, e = s.writer.ExecContext(ctx, "UPDATE owner_events SET at=0"); e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	page, e = s.OwnerEvents(ctx, page.Next, 100)
	if e != nil || !page.Reset || len(page.Items) != 0 || page.Next == 0 {
		t.Fatal("expired cursor cannot recover", page, e)
	}
	next, e := s.OwnerEvents(ctx, page.Next, 100)
	if e != nil || next.Reset {
		t.Fatal("permanent cursor reset", next, e)
	}
	if n := countTest(t, s, "SELECT count(*) FROM authority_items WHERE kind='operations'"); n != 1 {
		t.Fatal("history erased authority", n)
	}
	if _, e = c.Decide(ctx, request.ID, false); e != nil {
		t.Fatal(e)
	}
	if _, e = s.writer.ExecContext(ctx, "UPDATE owner_operation_times SET updated=0"); e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	items, _, e = s.OwnerOperations(ctx, "", 10, false)
	if e != nil || len(items) != 0 {
		t.Fatal("completed display history not pruned", items, e)
	}
	if n := countTest(t, s, "SELECT count(*) FROM authority_items WHERE kind='operations'"); n != 1 {
		t.Fatal("completed replay fact lost", n)
	}
	if _, e = c.Propose(agent, request); e != nil {
		t.Fatal("replay after pruning failed", e)
	}
	if n := countTest(t, s, "SELECT count(*) FROM owner_operation_times"); n != 0 {
		t.Fatal("replay revived expired history", n)
	}
}

func TestOwnerDraftBackupRestoreAndCredentialRecovery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	opts := deploymentOptions()
	s, e := OpenDeployment(ctx, root, opts)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	a, e := s.OpenControlAuthority(ctx, ownerInitial())
	if e != nil {
		t.Fatal(e)
	}
	c := informationcontrol.New(a)
	token, e := c.InitializeOwner("Owner")
	if e != nil {
		t.Fatal(e)
	}
	owner := informationcontrol.Authenticate(ctx, token)
	p, agent := workAgent(t, c, owner)
	d := newDraft(t, s, owner, "durable private backup")
	g, e := s.GrantDraft(owner, d.ID, p.ID, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	putRetrievalAsset(t, s, domain.Information{ID: "evidence", Revision: 1, Content: "source evidence", Source: domain.Source{Ref: "source-document"}})
	edit, e := s.CreateDraft(owner, contract.DraftInput{Target: contract.AssetVersion{ID: "evidence", Revision: 1}, Content: StringSource("owner revision")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PublishDraft(owner, edit.ID, edit.Revision, contract.OperationIdentity{ID: "revise-source"}); e != nil {
		t.Fatal(e)
	}
	if e = s.ExportArchive(owner, archive); e != nil {
		t.Fatal(e)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if _, e = RestoreArchive(ctx, archive, destination, deploymentOptions().Options); e != nil {
		t.Fatal(e)
	}
	r, e := OpenDeployment(ctx, destination, deploymentOptions())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if _, body, e := r.ReadDraft(owner, d.ID, ""); !errors.Is(e, ErrAccess) {
		if body != nil {
			body.Close()
		}
		t.Fatal("old owner credential restored", e)
	}
	if _, body, e := r.ReadDraft(agent, d.ID, g.ID); !errors.Is(e, ErrAccess) {
		if body != nil {
			body.Close()
		}
		t.Fatal("old agent credential restored", e)
	}
	if countTest(t, r, "SELECT count(*) FROM owner_draft_grants") != 0 {
		t.Fatal("historical work grants restored")
	}
	ra, e := r.OpenControlAuthority(ctx, ownerInitial())
	if e != nil {
		t.Fatal(e)
	}
	recovered, e := informationcontrol.New(ra).RecoverOwner()
	if e != nil {
		t.Fatal(e)
	}
	if got := draftText(t, r, informationcontrol.Authenticate(ctx, recovered), d.ID, ""); got != "durable private backup" {
		t.Fatal(got)
	}
	_, original, e := r.OpenOriginal(informationcontrol.Authenticate(ctx, recovered), "evidence", false)
	if e != nil {
		t.Fatal(e)
	}
	originalText, e := io.ReadAll(original)
	original.Close()
	if e != nil || string(originalText) != "source evidence" {
		t.Fatal("backup lost retained evidence", string(originalText), e)
	}
	if got := readTest(t, r, "evidence"); got != "owner revision" {
		t.Fatal("backup changed current revision", got)
	}
	// Recovery of the current owner also invalidates grants in the live store.
	if _, e = c.RecoverOwner(); e != nil {
		t.Fatal(e)
	}
	if _, body, e := s.ReadDraft(agent, d.ID, g.ID); !errors.Is(e, ErrAccess) {
		if body != nil {
			body.Close()
		}
		t.Fatal("grant survived owner recovery", e)
	}
}

func TestOwnerDraftSchemaUpgradeAndFutureRejection(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	stopMaintenance(s)
	path := s.path
	op := operation("before-upgrade")
	v := stageTest(t, s, op, "existing", "existing content", 1)
	if e := s.Publish(ctx, receipt(op, v), []AssetWrite{v}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.writer.ExecContext(ctx, `DROP TABLE owner_draft_grants; DROP TABLE owner_drafts; DROP TABLE asset_originals; DROP TABLE owner_event_relation_changes;
 DROP TABLE owner_events; DROP TABLE owner_operation_times;
 DELETE FROM store_meta WHERE key LIKE 'owner_%'; UPDATE store_meta SET value=5 WHERE key='format'`); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	s = openOwnerTest(t, path)
	if got := readTest(t, s, "existing"); got != "existing content" {
		t.Fatal(got)
	}
	if _, e := s.OpenControlAuthority(ctx, ownerInitial()); e != nil {
		t.Fatal(e)
	}
	newDraft(t, s, ctx, "after upgrade")
	if countTest(t, s, "SELECT value FROM store_meta WHERE key='format'") != 6 {
		t.Fatal("schema not upgraded")
	}
	if e := s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "UPDATE store_meta SET value=999 WHERE key='format'")
		return e
	}); e != nil {
		t.Fatal(e)
	}
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	future, e := Open(ctx, path, deploymentOptions().Options)
	if e == nil {
		future.Close()
		t.Fatal("future format accepted")
	}
}

func TestOwnerPublicationReceiptRotation(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	stopMaintenance(s)
	e := s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `WITH RECURSIVE n(i) AS(SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<4095)
 INSERT INTO operation_receipts SELECT 'seed','seed',cast(i AS TEXT),1,'seed','seed','[]' FROM n`)
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	d := newDraft(t, s, ctx, "last receipt before rotation")
	a, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "last"})
	if e != nil {
		t.Fatal(e)
	}
	retry, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "last"})
	if e != nil || retry != a {
		t.Fatal("last receipt retry lost", retry, e)
	}
	lookedUp, e := s.OwnerPublication(ctx, "last")
	if e != nil || lookedUp.State != "completed" || lookedUp.Asset != a {
		t.Fatal("publication recovery at receipt capacity", lookedUp, e)
	}
	if n := countTest(t, s, "SELECT count(*) FROM operation_receipts"); n != 4096 {
		t.Fatal("read-only recovery rotated receipts", n)
	}
	d = newDraft(t, s, ctx, "next independent publication")
	if _, e = s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "next"}); e != nil {
		t.Fatal("capacity stopped publication", e)
	}
	if n := countTest(t, s, "SELECT value FROM store_meta WHERE key='operation_generation'"); n != 2 {
		t.Fatal(n)
	}
	lookedUp, e = s.OwnerPublication(ctx, "last")
	if e != nil || lookedUp.State != "unknown" || lookedUp.Asset.ID != "" {
		t.Fatal("expired receipt was treated as an uncommitted publication", lookedUp, e)
	}
	d = newDraft(t, s, ctx, "expired explicit operation")
	if _, e = s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "expired", Generation: 1}); !errors.Is(e, contract.ErrOperationExpired) {
		t.Fatal("expired identity accepted", e)
	}
}

func TestOwnerDraftMaintenanceFailureBackpressure(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	stopMaintenance(s)
	d := newDraft(t, s, ctx, "original draft")
	failure := errors.New("injected maintenance failure")
	s.maintenanceMu.Lock()
	s.maintenanceErr = failure
	s.maintenanceMu.Unlock()
	before := countTest(t, s, "SELECT count(*) FROM payloads")
	if _, e := s.WriteDraft(ctx, contract.DraftWrite{ID: d.ID, ExpectedRevision: d.Revision, Content: StringSource("replacement")}); !errors.Is(e, failure) {
		t.Fatal("maintenance failure ignored", e)
	}
	if n := countTest(t, s, "SELECT count(*) FROM payloads"); n != before {
		t.Fatal("failure added payload")
	}
	if got := draftText(t, s, ctx, d.ID, ""); got != "original draft" {
		t.Fatal(got)
	}
	if e := s.DiscardDraft(ctx, d.ID, d.Revision); e != nil {
		t.Fatal("cleanup blocked by backpressure", e)
	}
}

func TestOwnerOriginalOpenReaderRevocationAndForget(t *testing.T) {
	for _, mode := range []string{"revoke", "forget"} {
		t.Run(mode, func(t *testing.T) {
			s, c, ctx := ownerFixture(t)
			p, agent := workAgent(t, c, ctx)
			op := operation("source")
			v := stageTest(t, s, op, "source", "private original body", 1)
			s.Abandon(ctx, v.Payload)
			var e error
			v.Payload, e = s.Stage(ctx, operationKey(op), StringSource("private original body"), StringSource(`{"source":{"ref":"source-document"}}`))
			if e != nil {
				t.Fatal(e)
			}
			if e = s.Publish(ctx, receipt(op, v), []AssetWrite{v}); e != nil {
				t.Fatal(e)
			}
			d, e := s.CreateDraft(ctx, contract.DraftInput{Target: contract.AssetVersion{ID: "source", Revision: 1}, Content: StringSource("owner current")})
			if e != nil {
				t.Fatal(e)
			}
			a, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "revise"})
			if e != nil {
				t.Fatal(e)
			}
			_, r, e := s.OpenOriginal(agent, "source", false)
			if e != nil {
				t.Fatal(e)
			}
			defer r.Close()
			if _, e = r.Read(make([]byte, 1)); e != nil {
				t.Fatal(e)
			}
			if mode == "revoke" {
				e = c.SetPermissions(ctx, p.ID, nil)
			} else {
				if e = s.StageForget(ctx, "forget", []ForgetTarget{{a.ID, a.Revision}}); e == nil {
					e = s.CommitForget(ctx, "forget")
				}
			}
			if e != nil {
				t.Fatal(e)
			}
			if n, e := r.Read(make([]byte, 20)); e == nil || n != 0 {
				t.Fatal("reader delivered after stop-use", n, e)
			}
		})
	}
}

func TestOwnerOriginalRequiresAuthenticatedReadPermission(t *testing.T) {
	s, c, owner := ownerFixture(t)
	p, reader := workAgent(t, c, owner)
	op := operation("source-auth")
	v := stageTest(t, s, op, "source-auth", "original body", 1)
	if e := s.Abandon(owner, v.Payload); e != nil {
		t.Fatal(e)
	}
	var e error
	v.Payload, e = s.Stage(owner, operationKey(op), StringSource("original body"), StringSource(`{"source":{"ref":"original-source"}}`))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Publish(owner, receipt(op, v), []AssetWrite{v}); e != nil {
		t.Fatal(e)
	}
	d, e := s.CreateDraft(owner, contract.DraftInput{Target: contract.AssetVersion{ID: v.Meta.ID, Revision: 1}, Content: StringSource("current body")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PublishDraft(owner, d.ID, d.Revision, contract.OperationIdentity{ID: "publish-auth"}); e != nil {
		t.Fatal(e)
	}
	for _, valid := range []context.Context{owner, reader} {
		_, r, e := s.OpenOriginal(valid, v.Meta.ID, false)
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r)
		r.Close()
		if e != nil || string(b) != "original body" {
			t.Fatal(string(b), e)
		}
	}
	if e = c.SetPermissions(owner, p.ID, []contract.Permission{contract.MaintainPermission}); e != nil {
		t.Fatal(e)
	}
	for name, unauthorized := range map[string]context.Context{
		"absent":                  context.Background(),
		"malformed":               contract.WithAuthenticationDigest(context.Background(), "invalid"),
		"unknown":                 contract.WithAuthenticationDigest(context.Background(), strings.Repeat("0", 64)),
		"without read permission": reader,
	} {
		t.Run(name, func(t *testing.T) {
			for _, details := range []bool{false, true} {
				for _, id := range []string{v.Meta.ID, "not-an-asset"} {
					revision, r, e := s.OpenOriginal(unauthorized, id, details)
					if r != nil {
						r.Close()
					}
					if !errors.Is(e, ErrAccess) || revision != 0 || r != nil {
						t.Fatalf("unauthorized original returned: revision=%d reader=%v error=%v", revision, r, e)
					}
				}
			}
		})
	}
}

func TestOwnerHistoryExpiresOnReadWithoutMaintenance(t *testing.T) {
	s, c, ctx := ownerFixture(t)
	stopMaintenance(s)
	p, agent := workAgent(t, c, ctx)
	req := contract.ManagementRequest{ID: "old", Operation: "permissions", SubjectID: p.ID, Permissions: []contract.Permission{contract.ReadPermission}}
	if _, e := c.Propose(agent, req); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Decide(ctx, req.ID, false); e != nil {
		t.Fatal(e)
	}
	if _, e := s.writer.ExecContext(ctx, "UPDATE owner_events SET at=0; UPDATE owner_operation_times SET updated=0"); e != nil {
		t.Fatal(e)
	}
	page, e := s.OwnerEvents(ctx, 1, 100)
	if e != nil || len(page.Items) != 0 || !page.Reset {
		t.Fatal(page, e)
	}
	items, _, e := s.OwnerOperations(ctx, "", 100, false)
	if e != nil || len(items) != 0 {
		t.Fatal(items, e)
	}
	if countTest(t, s, "SELECT count(*) FROM owner_events") != 2 {
		t.Fatal("fixture failed to retain expired physical rows")
	}
	// Age alone changes projection visibility, even when maintenance has not
	// advanced an epoch. Capture a near-future retention boundary then cross it.
	expires := time.Now().Add(150 * time.Millisecond)
	stamp := expires.Add(-OwnerHistoryRetention).UnixMilli()
	if _, e = s.writer.ExecContext(ctx, "UPDATE owner_events SET at=?; UPDATE owner_operation_times SET updated=?", stamp, stamp); e != nil {
		t.Fatal(e)
	}
	before, e := s.OwnerCheckpoint(ctx)
	if e != nil || before.VisibleUntil == 0 {
		t.Fatal(before, e)
	}
	time.Sleep(time.Until(expires) + 10*time.Millisecond)
	after, e := s.OwnerCheckpoint(ctx)
	if e != nil || after.VisibleUntil != 0 || before.Events != after.Events {
		t.Fatal("idle expiration not observable", before, after, e)
	}
}

func TestOwnerRestoreSnapshotFromPreviousSchema(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	snapshot, e := s.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	old := filepath.Join(t.TempDir(), "v5.sqlite")
	b, e := os.ReadFile(snapshot.Path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(old, b, 0600); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite", old)
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`DROP TABLE owner_draft_grants; DROP TABLE owner_drafts; DROP TABLE asset_originals; DROP TABLE owner_event_relation_changes; DROP TABLE owner_events; DROP TABLE owner_operation_times;
 DELETE FROM store_meta WHERE key LIKE 'owner_%'; UPDATE store_meta SET value=5 WHERE key='format'`)
	closeErr := db.Close()
	if e != nil || closeErr != nil {
		t.Fatal(e, closeErr)
	}
	digest, e := sourceDigest(ctx, old)
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.RestoreSnapshot(ctx, StorageSnapshot{Path: old, SHA256: digest}, filepath.Join(t.TempDir(), "restored.sqlite"), deploymentOptions().Options)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if countTest(t, r, "SELECT value FROM store_meta WHERE key='format'") != schemaVersion {
		t.Fatal("restored old schema not upgraded")
	}
}

func TestOwnerGrantRevocationAndHandoffCancellationAreAtomic(t *testing.T) {
	s, c, ctx := ownerFixture(t)
	p, agent := workAgent(t, c, ctx)
	d, e := s.CreateDraft(ctx, contract.DraftInput{Content: StringSource("private grant")})
	if e != nil {
		t.Fatal(e)
	}
	g, e := s.GrantDraft(ctx, d.ID, p.ID, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	target := contract.Location{SystemID: c.SystemID(), ServiceID: "next", Endpoint: "https://next.test", Certificate: "fixture", Composition: "composition"}
	h, e := c.PrepareHandoff(ctx, "atomic-grant", target)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.DecideHandoff(ctx, h.ID, h.Revision, true); e != nil {
		t.Fatal(e)
	}
	if _, e = c.FreezeHandoff(ctx, h.ID, true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.writer.ExecContext(ctx, `CREATE TRIGGER fail_owner_revoke BEFORE UPDATE ON authority_header BEGIN SELECT RAISE(ABORT,'injected cancel failure'); END`); e != nil {
		t.Fatal(e)
	}
	if e = s.RevokeDraftGrant(ctx, g.ID); e == nil {
		t.Fatal("injected cancellation failure ignored")
	}
	if _, e = s.DraftMetadata(agent, d.ID, g.ID); e != nil {
		t.Fatal("grant partially revoked", e)
	}
	state := c.State()
	if state.Access.Handoff == nil || state.Access.Handoff.Phase != "frozen" || len(state.Access.Cancelled) != 0 {
		t.Fatal("partial cancellation", state.Access)
	}
	if _, e = s.writer.ExecContext(ctx, "DROP TRIGGER fail_owner_revoke"); e != nil {
		t.Fatal(e)
	}
	if e = s.RevokeDraftGrant(ctx, g.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DraftMetadata(agent, d.ID, g.ID); e == nil {
		t.Fatal("revoked grant survived")
	}
	state = c.State()
	if state.Access.Handoff != nil || len(state.Access.Cancelled) != 1 {
		t.Fatal("no durable cancellation", state.Access)
	}
}

func TestOwnerConcurrentDraftMutationsUseWholeWorkspace(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	const n = 32
	drafts := make([]contract.Draft, n)
	for i := range drafts {
		drafts[i] = newDraft(t, s, ctx, "initial")
	}
	start := make(chan struct{})
	errs := make(chan error, n)
	for _, d := range drafts {
		go func(d contract.Draft) {
			<-start
			_, e := s.WriteDraft(ctx, contract.DraftWrite{ID: d.ID, ExpectedRevision: d.Revision, Append: true, Content: StringSource(" appended")})
			errs <- e
		}(d)
	}
	close(start)
	for range n {
		if e := <-errs; e != nil {
			t.Fatal(e)
		}
	}
	if s.budget.Used() > 16*resourcebudget.MiB {
		t.Fatal("exceeded shared workspace")
	}
}

func TestOwnerLegacyControlImportDoesNotFabricateEvents(t *testing.T) {
	s, c, ctx := ownerFixture(t)
	p, agent := workAgent(t, c, ctx)
	req := contract.ManagementRequest{ID: "historical", Operation: "permissions", SubjectID: p.ID, Permissions: []contract.Permission{contract.ReadPermission}}
	if _, e := c.Propose(agent, req); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Decide(ctx, req.ID, false); e != nil {
		t.Fatal(e)
	}
	a, e := s.OpenControlAuthority(ctx, ownerInitial())
	if e != nil {
		t.Fatal(e)
	}
	var buf bytes.Buffer
	if e = a.ExportControl(ctx, &buf); e != nil {
		t.Fatal(e)
	}
	legacy := openOwnerTest(t, filepath.Join(t.TempDir(), "legacy.sqlite"))
	payload, e := legacy.Stage(ctx, "old-control", StringSource(buf.String()), nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = legacy.PublishControl(ctx, ControlRecord{Key: "authority", Revision: 1, Payload: payload}, 0); e != nil {
		t.Fatal(e)
	}
	if _, e = legacy.OpenControlAuthority(ctx, ownerInitial()); e != nil {
		t.Fatal(e)
	}
	if n := countTest(t, legacy, "SELECT count(*) FROM owner_events"); n != 0 {
		t.Fatal("migration fabricated present-day actions", n)
	}
	if n := countTest(t, legacy, "SELECT count(*) FROM owner_operation_times"); n != 1 {
		t.Fatal("lost bounded historical projection", n)
	}
}

func TestOwnerRelationNoticeUpgradeAtomicityAndRetention(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	stopMaintenance(s)
	putRetrievalAsset(t, s, domain.Information{ID: "target", Revision: 1, Content: "target fact"})
	putRetrievalAsset(t, s, domain.Information{ID: "source", Revision: 1, Content: "quotation", Relations: []domain.ExplicitRelation{{Type: "qualifies", TargetID: "target", Selector: &domain.TextSelector{Exact: "quotation"}}}})
	beforeEvents := countTest(t, s, "SELECT count(*) FROM owner_events")
	// Simulate the already-reviewed format 6, before relation notices existed.
	if _, e := s.writer.ExecContext(ctx, "DROP TABLE owner_event_relation_changes"); e != nil {
		t.Fatal(e)
	}
	path := s.path
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	s = openOwnerTest(t, path)
	stopMaintenance(s)
	events, e := s.OwnerEvents(ctx, 0, 100)
	if e != nil || int64(len(events.Items)) != beforeEvents {
		t.Fatal("old events unavailable after compatible upgrade", events, e)
	}
	for _, event := range events.Items {
		if event.InvalidatedRelations != nil {
			t.Fatal("invented notice for historical event", event)
		}
	}
	d, e := s.CreateDraft(ctx, contract.DraftInput{Target: contract.AssetVersion{ID: "source", Revision: 1}, Content: StringSource("replacement")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.writer.ExecContext(ctx, `CREATE TRIGGER fail_relation_notice BEFORE INSERT ON owner_event_relation_changes BEGIN SELECT RAISE(ABORT,'notice failure'); END`); e != nil {
		t.Fatal(e)
	}
	op := contract.OperationIdentity{ID: "notice-publication"}
	if _, e = s.PublishDraft(ctx, d.ID, d.Revision, op); e == nil {
		t.Fatal("notice failure did not abort publication")
	}
	if readTest(t, s, "source") != "quotation" || draftText(t, s, ctx, d.ID, "") != "replacement" {
		t.Fatal("failed publication changed source or lost draft")
	}
	if countTest(t, s, "SELECT count(*) FROM owner_events") != beforeEvents || countTest(t, s, "SELECT count(*) FROM owner_event_relation_changes") != 0 {
		t.Fatal("notice failure left partial events")
	}
	if _, e = s.writer.ExecContext(ctx, "DROP TRIGGER fail_relation_notice"); e != nil {
		t.Fatal(e)
	}
	a, e := s.PublishDraft(ctx, d.ID, d.Revision, op)
	if e != nil {
		t.Fatal(e)
	}
	if again, e := s.PublishDraft(ctx, d.ID, d.Revision, op); e != nil || again != a {
		t.Fatal("publication retry", again, e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s = openOwnerTest(t, path)
	stopMaintenance(s)
	events, e = s.OwnerEvents(ctx, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	notices := 0
	for _, event := range events.Items {
		if event.InvalidatedRelations != nil {
			notices++
			if event.Operation != op.ID || event.Kind != "draft_published" || *event.InvalidatedRelations != (contract.RelationInvalidationCounts{QuoteMissing: 1}) {
				t.Fatal("wrong durable notice", event)
			}
		}
	}
	if notices != 1 {
		t.Fatal("notice lost or duplicated", notices)
	}
	if _, e = s.writer.ExecContext(ctx, "UPDATE owner_events SET at=0"); e != nil {
		t.Fatal(e)
	}
	drainTest(t, s)
	if countTest(t, s, "SELECT count(*) FROM owner_events") != 0 || countTest(t, s, "SELECT count(*) FROM owner_event_relation_changes") != 0 {
		t.Fatal("expired event retained its relation notice")
	}
}

func TestOwnerStreamsRejectInheritedSnapshots(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	putRetrievalAsset(t, s, domain.Information{ID: "source", Revision: 1, Content: "original body", Source: domain.Source{Ref: "evidence"}})
	d, e := s.CreateDraft(ctx, contract.DraftInput{Target: contract.AssetVersion{ID: "source", Revision: 1}, Content: StringSource("current body")})
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "revise"})
	if e != nil {
		t.Fatal(e)
	}
	work := newDraft(t, s, ctx, "private draft")
	e = s.WithSnapshot(ctx, func(snapshotCtx context.Context) error {
		for _, details := range []bool{false, true} {
			rev, r, e := s.OpenOriginal(snapshotCtx, a.ID, details)
			if r != nil {
				r.Close()
			}
			if !errors.Is(e, errOwnerStreamSnapshot) || rev != 0 || r != nil {
				t.Fatal("original inherited stale snapshot", rev, e)
			}
		}
		got, r, e := s.ReadDraft(snapshotCtx, work.ID, "")
		if r != nil {
			r.Close()
		}
		if !errors.Is(e, errOwnerStreamSnapshot) || got != (contract.Draft{}) || r != nil {
			t.Fatal("draft inherited stale snapshot", got, e)
		}
		if _, e = s.CreateDraft(snapshotCtx, contract.DraftInput{Target: a}); !errors.Is(e, errOwnerStreamSnapshot) {
			t.Fatal("source copy inherited stale snapshot", e)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if countTest(t, s, "SELECT count(*) FROM owner_drafts") != 1 || draftText(t, s, ctx, work.ID, "") != "private draft" {
		t.Fatal("rejected call changed draft state")
	}
	_, r, e := s.OpenOriginal(ctx, a.ID, false)
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(r)
	r.Close()
	if e != nil || string(b) != "original body" {
		t.Fatal("normal original reader failed", string(b), e)
	}
}
