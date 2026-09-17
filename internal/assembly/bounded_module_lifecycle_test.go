package assembly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestModuleReviewForgottenPendingResultIsCleaned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	root := t.TempDir()
	r, e := openBoundedWith(Request{DataDir: root, ProductSemantics: Basic}, testManifest(t, Basic), productionResources)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	token, e := r.UserControl().InitializeOwner("test owner")
	if e != nil {
		t.Fatal(e)
	}
	ctx = informationcontrol.Authenticate(ctx, token)
	marker := "MODULE_REVIEW_SYNTHETIC_FORGET_MARKER_"
	body := strings.Repeat(marker, 400)
	made, e := r.Product().Create(ctx, contract.CreateInput{Content: body})
	if e != nil {
		t.Fatal(e)
	}
	args, _ := json.Marshal(map[string]string{"id": made.Information.ID})
	pending, e := r.Streaming().ExecuteStream(ctx, contract.StreamRequest{Operation: "ownward_read", Arguments: boundedstore.StringSource(args)})
	if e != nil {
		t.Fatal(e)
	}
	defer pending.Close()
	receipt, e := r.Management().Manage(ctx, contract.ManagementRequest{ID: "forget-pending-result", Operation: "forget", Targets: []contract.AssetVersion{{ID: made.Information.ID, Revision: 1}}})
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for receipt.Status != "completed" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		receipt, e = r.Management().Receipt(ctx, "forget-pending-result")
		if e != nil {
			t.Fatal(e)
		}
	}
	if receipt.Status != "completed" {
		t.Fatal("cleanup did not complete", receipt.Status)
	}
	deliveryError := pending.Check(ctx)
	_, readError := r.Product().Read(ctx, made.Information.ID)
	retained := 0
	e = filepath.WalkDir(filepath.Join(root, "scratch"), func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		v, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		if bytes.Contains(v, []byte(marker)) {
			retained++
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("status=%s old_delivery_rejected=%v current_read_rejected=%v scratch_files_with_forgotten_text=%d", receipt.Status, deliveryError != nil, readError != nil, retained)
	if deliveryError == nil || readError == nil {
		t.Fatal("stop-use barrier did not hold")
	}
	if retained > 0 {
		t.Error("completed forget retained controlled, undelivered original text")
	}
}

func TestModuleReviewRebuildActuallyRebuildsGeneration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resources := productionResources
	resources.openVector = func(string, composition.Manifest) (contract.VectorCapability, error) {
		return embedding.HashForTesting{Dimensions: 512}, nil
	}
	r, e := openBoundedWith(Request{DataDir: t.TempDir(), ProductSemantics: Collaborative, VectorBundleDir: t.TempDir()}, testManifest(t, Collaborative), resources)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	token, e := r.UserControl().InitializeOwner("test owner")
	if e != nil {
		t.Fatal(e)
	}
	ctx = informationcontrol.Authenticate(ctx, token)
	made, e := r.Product().Create(ctx, contract.CreateInput{Content: "rebuild preserves the original source"})
	if e != nil {
		t.Fatal(e)
	}
	before, _, e := r.Streaming().Store.Generation(ctx)
	if e != nil {
		t.Fatal(e)
	}
	counts, e := r.Kernel().Maintain(ctx, true)
	if e != nil {
		t.Fatal(e)
	}
	after, _, e := r.Streaming().Store.Generation(ctx)
	if e != nil {
		t.Fatal(e)
	}
	got, e := r.Product().Read(ctx, made.Information.ID)
	if e != nil || got.Content != made.Information.Content {
		t.Fatal("original changed", e)
	}
	t.Logf("successful_rebuild_generation_changed=%v counts=%v", before != after, counts)
	if before == after {
		t.Error("successful public rebuild did not build or activate a replacement generation")
	}
}

// This child exits without running deferred result/runtime cleanup. The only
// credential is a synthetic test owner token passed in memory through stdin.
func TestModuleReviewCrashScratchSurvivesRestartForget(t *testing.T) {
	if os.Getenv("OWNWARD_MODULE_REVIEW_CRASH_CHILD") == "1" {
		var input struct{ Root, Token, ID string }
		if e := json.NewDecoder(os.Stdin).Decode(&input); e != nil {
			t.Fatal(e)
		}
		r, e := openBoundedWith(Request{DataDir: input.Root, ProductSemantics: Basic}, testManifest(t, Basic), productionResources)
		if e != nil {
			t.Fatal(e)
		}
		ctx := informationcontrol.Authenticate(context.Background(), input.Token)
		args, _ := json.Marshal(map[string]string{"id": input.ID})
		result, e := r.Streaming().ExecuteStream(ctx, contract.StreamRequest{Operation: "ownward_read", Arguments: boundedstore.StringSource(args)})
		if e != nil {
			t.Fatal(e)
		}
		if e = result.Check(ctx); e != nil {
			t.Fatal(e)
		}
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	manifest := testManifest(t, Basic)
	open := func() (*Runtime, error) {
		return openBoundedWith(Request{DataDir: root, ProductSemantics: Basic}, manifest, productionResources)
	}
	r, e := open()
	if e != nil {
		t.Fatal(e)
	}
	token, e := r.UserControl().InitializeOwner("synthetic crash owner")
	if e != nil {
		t.Fatal(e)
	}
	ctx = informationcontrol.Authenticate(ctx, token)
	marker := "MODULE_REVIEW_SYNTHETIC_CRASH_MARKER_"
	made, e := r.Product().Create(ctx, contract.CreateInput{Content: strings.Repeat(marker, 400)})
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	input, _ := json.Marshal(struct{ Root, Token, ID string }{root, token, made.Information.ID})
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestModuleReviewCrashScratchSurvivesRestartForget$", "-test.timeout=12s")
	child.Env = append(os.Environ(), "OWNWARD_MODULE_REVIEW_CRASH_CHILD=1")
	child.Stdin = bytes.NewReader(input)
	if output, e := child.CombinedOutput(); e != nil {
		t.Fatalf("isolated crash child: %v %s", e, output)
	}
	count := func() int {
		n := 0
		e := filepath.WalkDir(filepath.Join(root, "scratch"), func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			v, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			if bytes.Contains(v, []byte(marker)) {
				n++
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		return n
	}
	orphanBefore := count()
	if orphanBefore != 0 {
		t.Fatal("OS did not clean crashed scratch")
	}
	// A legacy process could leave named scratch; recover it under the store lock.
	if e = os.WriteFile(filepath.Join(root, "scratch", "rpc-body-1234567890"), []byte(marker), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "scratch", "keep-user-file"), []byte("unrelated"), 0600); e != nil {
		t.Fatal(e)
	}
	r, e = open()
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	orphanAfterOpen := count()
	if orphanAfterOpen != 0 {
		t.Fatal("legacy scratch not recovered")
	}
	if _, e = os.Stat(filepath.Join(root, "scratch", "keep-user-file")); e != nil {
		t.Fatal("unrelated file removed", e)
	}
	receipt, e := r.Management().Manage(ctx, contract.ManagementRequest{ID: "forget-crash-orphan", Operation: "forget", Targets: []contract.AssetVersion{{ID: made.Information.ID, Revision: 1}}})
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	for receipt.Status != "completed" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		receipt, e = r.Management().Receipt(ctx, "forget-crash-orphan")
		if e != nil {
			t.Fatal(e)
		}
	}
	if receipt.Status != "completed" {
		t.Fatal("forget incomplete", receipt.Status)
	}
	_, readError := r.Product().Read(ctx, made.Information.ID)
	orphanAfterForget := count()
	t.Logf("orphan_before_restart=%d orphan_after_restart=%d orphan_after_completed_forget=%d current_read_rejected=%v", orphanBefore, orphanAfterOpen, orphanAfterForget, readError != nil)
	if readError == nil {
		t.Fatal("forgotten source remains readable")
	}
	if orphanAfterForget != 0 {
		t.Error("controlled source survives process exit, reopen, and completed forgetting")
	}
}

type moduleReviewRecoverVector struct {
	embedding.HashForTesting
	fail  bool
	calls int
}

func (v *moduleReviewRecoverVector) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	v.calls++
	if v.fail {
		return nil, errors.New("synthetic temporary local vector outage")
	}
	return v.HashForTesting.EmbedDocuments(ctx, texts)
}
func TestModuleReviewRebuildRecoversAcceptedLocalWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	vector := &moduleReviewRecoverVector{HashForTesting: embedding.HashForTesting{Dimensions: 512}, fail: true}
	resources := productionResources
	resources.openVector = func(string, composition.Manifest) (contract.VectorCapability, error) { return vector, nil }
	r, e := openBoundedWith(Request{DataDir: t.TempDir(), ProductSemantics: Collaborative, VectorBundleDir: t.TempDir()}, testManifest(t, Collaborative), resources)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	token, e := r.UserControl().InitializeOwner("synthetic recovery owner")
	if e != nil {
		t.Fatal(e)
	}
	ctx = informationcontrol.Authenticate(ctx, token)
	body := strings.Repeat("Long synthetic source for local recovery. ", 100)
	made, e := r.Product().Create(ctx, contract.CreateInput{Content: body})
	if e != nil {
		t.Fatal(e)
	}
	work, e := r.Streaming().SemanticWorkFor(ctx, []string{made.Information.ID})
	if e != nil || len(work) != 1 {
		t.Fatalf("fixture work count %d: %v", len(work), e)
	}
	w := work[0]
	sub := semantics.Submission{Schema: semantics.SubmissionSchema, WorkID: w.ID, AssetID: w.Asset.ID, Revision: w.Asset.Revision, Capability: semantics.Capability{ID: "synthetic-frozen-result", Version: "1"}, Status: semantics.SubmissionComplete, Analysis: semantics.Analysis{Summary: "Synthetic source for local recovery.", Organization: &semantics.Organization{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{{ID: "whole"}}, Links: []semantics.GroundedLink{}}}}
	accepted, e := r.Streaming().SubmitSemantic(ctx, sub)
	if e != nil || accepted.Status != "pending" {
		t.Fatal("fixture did not persist local pending", accepted, e)
	}
	beforeGeneration, _, e := r.Streaming().Store.Generation(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Kernel().Maintain(ctx, true); e == nil {
		t.Fatal("rebuild hid local vector failure")
	}
	failedGeneration, _, e := r.Streaming().Store.Generation(ctx)
	if e != nil || failedGeneration != beforeGeneration {
		t.Fatal("failed rebuild replaced active generation", e)
	}
	if _, e = r.Kernel().Maintain(ctx, false); e != nil {
		t.Fatal("failed candidate reclamation", e)
	}
	vector.fail = false
	callsBefore := vector.calls
	_, e = r.Kernel().Maintain(ctx, true)
	if e != nil {
		t.Fatal(e)
	}
	rebuildCalls := vector.calls - callsBefore
	after, e := r.Streaming().Organization(made.Information.ID)
	if e != nil {
		t.Fatal(e)
	}
	queue, e := r.Streaming().SemanticWork(ctx, 20)
	if e != nil {
		t.Fatal(e)
	}
	got, e := r.Product().Read(ctx, made.Information.ID)
	if e != nil || got.Content != body {
		t.Fatal("original changed", e)
	}
	// Positive control: the same already accepted result is sufficient; no new AI
	// judgment, source change or additional capability is required for recovery.
	recovered, e := r.Streaming().SubmitSemantic(ctx, sub)
	if e != nil || recovered.Status != "ready" {
		t.Fatal("positive resubmission control failed", recovered, e)
	}
	t.Logf("successful_rebuild_status=%s rebuild_vector_calls=%d queued_ai_work=%d explicit_same_result_resubmit=%s original_exact=true", after.Status, rebuildCalls, len(queue), recovered.Status)
	if after.Status != "ready" {
		t.Error("public rebuild leaves accepted work pending despite restored local capability")
	}
}
