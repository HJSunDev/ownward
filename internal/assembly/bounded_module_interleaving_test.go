package assembly

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/rpcstream"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestBReviewForgetDuringTransportCopy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	r, e := openBoundedWith(Request{DataDir: t.TempDir(), ProductSemantics: Basic}, testManifest(t, Basic), productionResources)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	token, e := r.UserControl().InitializeOwner("synthetic transport owner")
	if e != nil {
		t.Fatal(e)
	}
	ctx = informationcontrol.Authenticate(ctx, token)
	made, e := r.Product().Create(ctx, contract.CreateInput{Content: strings.Repeat("synthetic forgotten transport source. ", 6000)})
	if e != nil {
		t.Fatal(e)
	}
	args, _ := json.Marshal(map[string]string{"id": made.Information.ID})
	result, e := r.Streaming().ExecuteStream(ctx, contract.StreamRequest{Operation: "ownward_read", Arguments: boundedstore.StringSource(args)})
	if e != nil {
		t.Fatal(e)
	}
	defer result.Close()
	budget, _ := resourcebudget.New(2*resourcebudget.MiB, 0)
	scope := rpcstream.New(t.TempDir(), budget, 4*resourcebudget.MiB)
	defer scope.Close()
	envelope := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ownward_read","arguments":` + string(args) + `}}`
	_, call, e := scope.Project(ctx, strings.NewReader(envelope))
	if e != nil {
		t.Fatal(e)
	}
	copied := make(chan error, 1)
	done := make(chan error, 1)
	resume := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	defer release()
	go func() {
		_, err := call.Result(ctx, func(w io.Writer) error {
			source, err := result.Value.Open(ctx)
			if err != nil {
				copied <- err
				return err
			}
			_, err = io.Copy(w, source)
			source.Close()
			copied <- err
			if err != nil {
				return err
			}
			// Model a scheduler pause immediately after copying, before the transport
			// can finish Build and invoke Retain. No production behavior is replaced.
			select {
			case <-resume:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, func() error { return result.Check(ctx) }, result.Retain)
		done <- err
	}()
	select {
	case e = <-copied:
		if e != nil {
			t.Fatal(e)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if scope.Disk.Used() < 10000 {
		t.Fatal("transport copy fixture did not contain long original")
	}
	receipt, e := r.Management().Manage(ctx, contract.ManagementRequest{ID: "forget-while-copying", Operation: "forget", Targets: []contract.AssetVersion{{ID: made.Information.ID, Revision: 1}}})
	if e != nil {
		t.Fatal(e)
	}
	observeUntil := time.Now().Add(1500 * time.Millisecond)
	for receipt.Status != "completed" && time.Now().Before(observeUntil) {
		time.Sleep(10 * time.Millisecond)
		receipt, e = r.Management().Receipt(ctx, "forget-while-copying")
		if e != nil {
			t.Fatal(e)
		}
	}
	charged := scope.Disk.Used()
	premature := receipt.Status == "completed" && charged > 10000
	t.Logf("during_copy_forget_status=%s retained_transport_bytes=%d", receipt.Status, charged)
	release()
	select {
	case e = <-done:
		if e == nil {
			t.Error("forgotten copy was accepted for later delivery")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for receipt.Status != "completed" {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
		receipt, e = r.Management().Receipt(ctx, "forget-while-copying")
		if e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("after_copy_unblocked retained_transport_bytes=%d", scope.Disk.Used())
	if scope.Disk.Used() != 0 {
		t.Error("failed handoff did not release transport allocation")
	}
	if premature {
		t.Error("forget reported completed while an in-progress transport copy still retained original text")
	}
}

func TestBReviewRebuildRestartAndPendingContinuation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resources := productionResources
	resources.openVector = func(string, composition.Manifest) (contract.VectorCapability, error) {
		return embedding.HashForTesting{Dimensions: 512}, nil
	}
	root, bundle := t.TempDir(), t.TempDir()
	manifest := testManifest(t, Collaborative)
	open := func() (*Runtime, error) {
		return openBoundedWith(Request{DataDir: root, ProductSemantics: Collaborative, VectorBundleDir: bundle}, manifest, resources)
	}
	r, e := open()
	if e != nil {
		t.Fatal(e)
	}
	defer func() { r.Close() }()
	token, e := r.UserControl().InitializeOwner("synthetic rebuild owner")
	if e != nil {
		t.Fatal(e)
	}
	ctx = informationcontrol.Authenticate(ctx, token)
	submit := func(id string) {
		work, e := r.Streaming().SemanticWorkFor(ctx, []string{id})
		if e != nil || len(work) != 1 {
			t.Fatalf("pending continuation work %d: %v", len(work), e)
		}
		w := work[0]
		sub := semantics.Submission{Schema: semantics.SubmissionSchema, WorkID: w.ID, AssetID: w.Asset.ID, Revision: w.Asset.Revision, Capability: semantics.Capability{ID: "synthetic-review", Version: "1"}, Status: semantics.SubmissionComplete, Analysis: semantics.Analysis{Summary: "Synthetic independent source.", Organization: &semantics.Organization{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{{ID: "whole"}}, Links: []semantics.GroundedLink{}}}}
		state, e := r.Streaming().SubmitSemantic(ctx, sub)
		if e != nil || state.Status != "ready" {
			t.Fatal("pending continuation submit", state, e)
		}
	}
	a, e := r.Product().Create(ctx, contract.CreateInput{Content: "Already organized synthetic source."})
	if e != nil {
		t.Fatal(e)
	}
	submit(a.Information.ID)
	b, e := r.Product().Create(ctx, contract.CreateInput{Content: strings.Repeat("Pending synthetic source. ", 100)})
	if e != nil {
		t.Fatal(e)
	}
	before, _, e := r.Streaming().Store.Generation(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Kernel().Maintain(ctx, true); e != nil {
		t.Fatal(e)
	}
	after, _, e := r.Streaming().Store.Generation(ctx)
	if e != nil || before == after {
		t.Fatal("no rebuild generation switch", e)
	}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	r, e = open()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Kernel().Maintain(ctx, false); e != nil {
		t.Fatal("reopened generation maintenance", e)
	}
	submit(b.Information.ID)
	for id, want := range map[string]string{a.Information.ID: a.Information.Content, b.Information.ID: b.Information.Content} {
		got, e := r.Product().Read(ctx, id)
		if e != nil || got.Content != want {
			t.Fatal("reopened original changed", e)
		}
		state, e := r.Streaming().Organization(id)
		if e != nil || state.Status != "ready" {
			t.Fatal("reopened organization unavailable", state, e)
		}
	}
	t.Log("rebuild_switched=true restart_maintenance_ok=true pending_continuation_ready=true originals_exact=true")
}
