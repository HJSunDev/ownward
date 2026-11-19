package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestSemanticSubmissionBindingRejectsEveryUntrustedIdentity(t *testing.T) {
	identity := semantics.Capability{ID: "declared-semantic", Version: "v1", Execution: "in-process"}
	work := semantics.Work{
		ID: "work-current", Asset: domain.Information{ID: "asset-current", Revision: 3},
		Candidates: []semantics.Candidate{{ID: "candidate-current", Revision: 7}},
	}
	valid := semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: work.Asset.ID,
		Revision: work.Asset.Revision, Capability: identity, Status: semantics.SubmissionComplete,
		Analysis: semantics.Analysis{Summary: "looks valid"},
	}
	mutations := map[string]func(*semantics.Submission){
		"schema":     func(value *semantics.Submission) { value.Schema = "forged" },
		"work":       func(value *semantics.Submission) { value.WorkID = "work-other" },
		"asset":      func(value *semantics.Submission) { value.AssetID = "asset-other" },
		"revision":   func(value *semantics.Submission) { value.Revision++ },
		"status":     func(value *semantics.Submission) { value.Status = semantics.SubmissionUncertain },
		"capability": func(value *semantics.Submission) { value.Capability.Version = "forged" },
		"non-candidate": func(value *semantics.Submission) {
			value.Analysis.Relations = []semantics.Relation{{Type: "supports", TargetID: "asset-other", Confidence: 1, Evidence: "looks valid"}}
		},
		"wrong-candidate": func(value *semantics.Submission) {
			value.Analysis.Relations = []semantics.Relation{{Type: "supports", TargetID: "candidate-current", TargetRevision: 8, Confidence: 1, Evidence: "looks valid"}}
		},
		"missing-evidence": func(value *semantics.Submission) {
			value.Analysis.Relations = []semantics.Relation{{Type: "supports", TargetID: "candidate-current", Confidence: 1}}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			actual := valid
			mutate(&actual)
			if err := validateSemanticSubmissionBinding(work, identity, actual); err == nil {
				t.Fatal("unbound semantic submission was accepted")
			}
		})
	}
}

type testSemanticCapability struct{ provider semantics.Provider }

func (value testSemanticCapability) Identity() semantics.Capability {
	return semantics.Capability{ID: value.provider.Name(), Version: "test-provider-v1", Execution: "in-process"}
}

func (value testSemanticCapability) Analyze(ctx context.Context, work semantics.Work) (semantics.Submission, error) {
	analysis, err := value.provider.Analyze(ctx, work.Asset, work.Candidates)
	if err != nil {
		return semantics.Submission{}, err
	}
	return semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: work.Asset.ID,
		Revision: work.Asset.Revision, Capability: value.Identity(), Status: semantics.SubmissionComplete,
		Analysis: analysis,
	}, nil
}

func TestLegacySemanticAdapterRemainsBindingEquivalent(t *testing.T) {
	semantic := testSemanticCapability{provider: semantics.Heuristic{}}
	asset := domain.Information{
		ID: "asset-current", Revision: 1,
		Relations: []domain.ExplicitRelation{{Type: "supports", TargetID: "candidate-current"}},
	}
	work := semantics.Work{
		ID: "work-current", Asset: asset,
		Candidates: []semantics.Candidate{{ID: "candidate-current", Revision: 2, Content: "candidate"}},
	}
	submission, err := semantic.Analyze(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSemanticSubmissionBinding(work, semantic.Identity(), submission); err != nil {
		t.Fatalf("legal legacy submission changed semantics: %v", err)
	}
}

func openAuthenticatedStreaming(t *testing.T) (context.Context, *StreamingAssets, *boundedstore.Store) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	budget, err := resourcebudget.New(20*resourcebudget.MiB, 4*resourcebudget.MiB)
	if err != nil {
		t.Fatal(err)
	}
	store, err := boundedstore.Open(ctx, filepath.Join(root, "test.sqlite"), boundedstore.Options{Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	embedder := embedding.HashForTesting{Dimensions: 512}
	if _, err := store.InitializeGeneration(ctx, embedder.Space().ID); err != nil {
		store.Close()
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	principal := contract.Principal{
		ID: "owner", Revision: 1, CredentialDigest: digest,
		Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission},
	}
	if err := store.PublishAccess(ctx, boundedstore.AccessHeader{System: "test-system", Revision: 1}, 0, []contract.Principal{principal}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	authCtx := contract.WithAuthenticationDigest(ctx, digest)
	t.Cleanup(func() { _ = store.Close() })
	return authCtx, &StreamingAssets{
		Store: store, Budget: budget, Scratch: root, DiskBytes: 128 * resourcebudget.MiB, Embedder: embedder,
	}, store
}

func withOp(t *testing.T, ctx context.Context, store *boundedstore.Store, id string) context.Context {
	t.Helper()
	gen, err := store.OperationGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return contract.WithOperation(ctx, contract.OperationIdentity{
		ID: id, System: "test-system", Principal: "owner", Generation: gen,
	})
}

func TestRejectedSemanticSubmissionCannotWriteDerivedOrganization(t *testing.T) {
	ctx, service, store := openAuthenticatedStreaming(t)
	target, err := service.Create(withOp(t, ctx, store, "op-target"), CreateInput{Kind: domain.KindKnowledge, Content: "trusted target"})
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.Create(withOp(t, ctx, store, "op-source"), CreateInput{
		Kind: domain.KindKnowledge, Content: "trusted source",
		Relations: []domain.ExplicitRelation{{Type: "related_to", TargetID: target.Information.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	works, err := service.SemanticWorkFor(ctx, []string{source.Information.ID})
	if err != nil || len(works) != 1 {
		t.Fatalf("expected pending semantic work, got %#v err=%v", works, err)
	}
	work := works[0]
	forged := semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: work.Asset.ID,
		Revision:   work.Asset.Revision,
		Capability: semantics.Capability{ID: "adversarial", Version: "v1", Execution: "external"},
		Status:     semantics.SubmissionComplete,
		Analysis: semantics.Analysis{
			Summary: "looks valid",
			Relations: []semantics.Relation{{
				Type: "supports", TargetID: "forged-target", Confidence: 1, Evidence: "looks valid",
			}},
		},
	}
	if _, err := service.SubmitSemantic(ctx, forged); err == nil {
		t.Fatal("forged non-candidate target relation was accepted")
	}
	orgState, err := service.Organization(source.Information.ID)
	if err != nil || orgState.Status != "pending" {
		t.Fatalf("rejected semantic submission altered pending organization state: %#v err=%v", orgState, err)
	}
	authoritative, err := service.Read(ctx, source.Information.ID)
	if err != nil || authoritative.Content != "trusted source" || len(authoritative.Relations) != 1 || authoritative.Relations[0].TargetID != target.Information.ID {
		t.Fatalf("semantic submission changed authoritative asset: %#v %v", authoritative, err)
	}
	if _, err := service.Read(ctx, "forged-target"); err == nil {
		t.Fatal("semantic submission created an authoritative target")
	}
}
