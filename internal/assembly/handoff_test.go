package assembly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
)

func TestHandoffPreservesAuthorityDerivedWorkAndMutationReceipt(t *testing.T) {
	manifest := testManifest(t, Collaborative)
	source := filepath.Join(t.TempDir(), "source")
	target := filepath.Join(t.TempDir(), "target")
	resources := resources{restore: authoritysubstrate.Restore, openAuthority: openTestAuthority, openVector: func(string, composition.Manifest) (contract.VectorCapability, error) {
		return embedding.Unavailable{}, nil
	}}
	open := func(path string) (*Runtime, error) {
		return openWith(Request{DataDir: path, ProductSemantics: Collaborative, VectorBundleDir: filepath.Join(t.TempDir(), "bundle")}, manifest, resources)
	}
	r, err := open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ownerToken, err := r.UserControl().InitializeOwner("owner")
	if err != nil {
		t.Fatal(err)
	}
	owner := informationcontrol.Authenticate(context.Background(), ownerToken)
	p, token, err := r.UserControl().Enroll(owner, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.UserControl().SetPermissions(owner, p.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission}); err != nil {
		t.Fatal(err)
	}
	ctx := informationcontrol.Authenticate(context.Background(), token)
	op := contract.OperationIdentity{ID: "saved-create", Generation: 1, Kind: "create", Digest: "original-input"}
	operation := contract.WithOperation(ctx, op)
	first, err := r.Product().Create(operation, contract.CreateInput{Kind: domain.KindGeneral, Content: "current information"})
	if err != nil {
		t.Fatal(err)
	}
	workBefore, err := r.Product().SemanticWork(ctx, 10)
	assetBefore, readErr := r.Product().Read(ctx, first.Information.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	location := contract.Location{SystemID: r.UserControl().SystemID(), ServiceID: "target", Endpoint: "https://target.test", Certificate: "test", Composition: manifest.Identity}
	if _, err := r.UserControl().PrepareHandoff(owner, "migration", location); err != nil {
		t.Fatal(err)
	}
	h, err := r.UserControl().FreezeHandoff(owner, "migration", true)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(t.TempDir(), "snapshot")
	os.MkdirAll(stage, 0700)
	if err := r.ExportHandoff(stage); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "transfer.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := authoritysubstrate.WriteHandoffArchive(stage, file); err != nil {
		t.Fatal(err)
	}
	file.Close()
	data, _ := os.ReadFile(archivePath)
	digest := sha256.Sum256(data)
	prepared, err := authoritysubstrate.StageHandoff(archivePath, target)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Access.Handoff.Phase != "frozen" {
		t.Fatal("candidate became active before source retirement")
	}
	permit, err := r.UserControl().RetireHandoff(owner, h.ID, hex.EncodeToString(digest[:]), h.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Product().Read(ctx, first.Information.ID); err == nil {
		t.Fatal("old source still delivered information")
	}
	if err := authoritysubstrate.ActivateHandoff(target, permit); err != nil {
		t.Fatal(err)
	}
	if err := authoritysubstrate.ActivateHandoff(target, permit); err != nil {
		t.Fatal("activation resume failed", err)
	}
	next, err := open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	actual, err := next.Product().Read(ctx, first.Information.ID)
	if err != nil || !reflect.DeepEqual(actual, assetBefore) {
		t.Fatalf("asset or credentials changed: err=%v before=%#v after=%#v", err, assetBefore, actual)
	}
	workAfter, err := next.Product().SemanticWork(ctx, 10)
	if err != nil || !reflect.DeepEqual(workBefore, workAfter) {
		t.Fatal("semantic work changed during location move", err)
	}
	retry, err := next.Product().Create(operation, contract.CreateInput{Kind: domain.KindGeneral, Content: "current information"})
	if err != nil || retry.Information.ID != first.Information.ID {
		t.Fatal("migration lost mutation receipt", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := open(source); err == nil {
		t.Fatal("retired source reopened as an active kernel")
	}
}
