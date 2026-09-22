package assembly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func TestBoundedRuntimeControlChangesBackupAndReopen(t *testing.T) {
	manifest := testManifest(t, Basic)
	dir := t.TempDir()
	open := func() (*Runtime, error) {
		return openBoundedWith(Request{DataDir: dir, ProductSemantics: Basic}, manifest, productionResources)
	}
	r, e := open()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	token, e := r.UserControl().InitializeOwner("Owner")
	if e != nil {
		t.Fatal(e)
	}
	ctx := informationcontrol.Authenticate(context.Background(), token)
	created, e := r.Product().Create(ctx, contract.CreateInput{Content: "Current original source"})
	if e != nil {
		t.Fatal(e)
	}
	id := created.Information.ID
	reader := r.UnderlyingKernel().(interface {
		ReadInformation(context.Context, string) (contract.InformationRead, error)
	})
	read, e := reader.ReadInformation(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	checker := r.UnderlyingKernel().(interface {
		CheckInformation(context.Context, []string) ([]contract.InformationCheck, error)
	})
	check, e := checker.CheckInformation(ctx, []string{read.Basis})
	if e != nil || len(check) != 1 || check[0].Status != "unchanged" {
		t.Fatal("unchanged basis", check, e)
	}
	content := "Updated original source"
	if _, e = r.Product().Update(ctx, contract.UpdateInput{ID: id, ExpectedRevision: 1, Content: &content}); e != nil {
		t.Fatal(e)
	}
	check, e = checker.CheckInformation(ctx, []string{read.Basis})
	if e != nil || check[0].Status != "changed" {
		t.Fatal("changed basis", check, e)
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	if e = r.Backup(archive); e != nil {
		t.Fatal(e)
	}
	b, _ := resourcebudget.New(20*resourcebudget.MiB, 4*resourcebudget.MiB)
	restored := filepath.Join(t.TempDir(), "restored")
	if _, e = boundedstore.RestoreArchive(context.Background(), archive, restored, boundedstore.Options{Budget: b}); e != nil {
		t.Fatal(e)
	}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	r, e = open()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	got, e := r.Product().Read(ctx, id)
	if e != nil || got.Content != content {
		t.Fatal("reopen changed original", got, e)
	}
	op, e := r.Management().Manage(ctx, contract.ManagementRequest{ID: "forget", Operation: "forget", Targets: []contract.AssetVersion{{ID: id, Revision: 2}}})
	if e != nil {
		t.Fatal(e)
	}
	if op.Status != "cleaning" && op.Status != "completed" {
		t.Fatal(op)
	}
	if _, e = r.Product().Read(ctx, id); e == nil {
		t.Fatal("forgotten source still readable")
	}
}

func TestBoundedHandoffKeepsIdentityAndReceipts(t *testing.T) {
	manifest := testManifest(t, Basic)
	open := func(dir string) (*Runtime, error) {
		return openBoundedWith(Request{DataDir: dir, ProductSemantics: Basic}, manifest, productionResources)
	}
	r, e := open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	token, e := r.UserControl().InitializeOwner("Owner")
	if e != nil {
		t.Fatal(e)
	}
	owner := informationcontrol.Authenticate(context.Background(), token)
	op := contract.WithOperation(owner, contract.OperationIdentity{ID: "saved", Generation: 1})
	created, e := r.Product().Create(op, contract.CreateInput{Content: "Handoff preserves original content"})
	if e != nil {
		t.Fatal(e)
	}
	location := contract.Location{SystemID: r.UserControl().SystemID(), ServiceID: "target", Endpoint: "https://target.test", Certificate: "test", Composition: manifest.Identity}
	draft, e := r.Streaming().Store.CreateDraft(owner, contract.DraftInput{Target: contract.AssetVersion{ID: created.Information.ID, Revision: 1}, Content: boundedstore.StringSource("private handoff work")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Product().Read(owner, draft.ID); e == nil {
		t.Fatal("private draft visible as asset")
	}
	p, agentToken, e := r.UserControl().Enroll(owner, "Draft writer")
	if e != nil {
		t.Fatal(e)
	}
	if e = r.UserControl().SetPermissions(owner, p.ID, []contract.Permission{contract.ReadPermission}); e != nil {
		t.Fatal(e)
	}
	grant, e := r.Streaming().Store.GrantDraft(owner, draft.ID, p.ID, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.UserControl().PrepareHandoff(owner, "move", location); e != nil {
		t.Fatal(e)
	}
	if _, e := r.UserControl().DecideHandoff(owner, "move", r.UserControl().State().Access.Handoff.Revision, true); e != nil {
		t.Fatal(e)
	}
	hand, e := r.UserControl().FreezeHandoff(owner, "move", true)
	if e != nil {
		t.Fatal(e)
	}
	stage := t.TempDir()
	if e = r.ExportHandoff(stage); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "transfer.zip")
	file, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = authoritysubstrate.WriteHandoffArchive(stage, file); e != nil {
		t.Fatal(e)
	}
	file.Close()
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(raw)
	target := filepath.Join(t.TempDir(), "received")
	staged, e := StageHandoff(path, target)
	if e != nil || staged.Access.Handoff.Phase != "frozen" {
		t.Fatal("staging", e)
	}
	permit, e := r.UserControl().RetireHandoff(owner, "move", hex.EncodeToString(sum[:]), hand.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if e = ActivateHandoff(target, permit); e != nil {
		t.Fatal(e)
	}
	if e = ActivateHandoff(target, permit); e != nil {
		t.Fatal(e)
	}
	next, e := open(target)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	got, e := next.Product().Read(owner, created.Information.ID)
	if e != nil || got.Content != created.Information.Content {
		t.Fatal("identity or data lost", e)
	}
	agent := informationcontrol.Authenticate(context.Background(), agentToken)
	_, body, e := next.Streaming().Store.ReadDraft(agent, draft.ID, grant.ID)
	if e != nil {
		t.Fatal("handoff lost authorized private work", e)
	}
	text, e := io.ReadAll(body)
	body.Close()
	if e != nil || string(text) != "private handoff work" {
		t.Fatal(string(text), e)
	}
	if _, body, e = r.Streaming().Store.ReadDraft(owner, draft.ID, ""); e == nil {
		body.Close()
		t.Fatal("retired source still exposes draft")
	}
	retry, e := next.Product().Create(op, contract.CreateInput{Content: "Handoff preserves original content"})
	if e != nil || retry.Information.ID != got.ID {
		t.Fatal("receipt lost", e)
	}
	if _, e = r.Product().Read(owner, got.ID); e == nil {
		t.Fatal("retired source remained active")
	}
}
