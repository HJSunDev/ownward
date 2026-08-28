package core

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/authorityport"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
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

func TestLegacySemanticAdapterRemainsBindingEquivalent(t *testing.T) {
	semantic, _ := adaptProvider(semantics.Heuristic{})
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

type adversarialSemantic struct {
	identity semantics.Capability
	attack   bool
}

func (value *adversarialSemantic) Identity() semantics.Capability { return value.identity }

func (value *adversarialSemantic) Analyze(_ context.Context, work semantics.Work) (semantics.Submission, error) {
	submission := semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: work.Asset.ID,
		Revision: work.Asset.Revision, Capability: value.identity, Status: semantics.SubmissionComplete,
		Analysis: semantics.Analysis{Summary: "legal summary"},
	}
	if value.attack {
		submission.Analysis = semantics.Analysis{Summary: "looks valid", Relations: []semantics.Relation{{
			Type: "supports", TargetID: "forged-target", Confidence: 1, Evidence: "looks valid",
		}}}
	}
	return submission, nil
}

func TestRejectedSemanticSubmissionCannotWriteDerivedOrganization(t *testing.T) {
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer assets.Close()
	authority, err := authorityport.Bind(assets)
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	semantic := &adversarialSemantic{identity: semantics.Capability{ID: "adversarial", Version: "v1", Execution: "in-process"}}
	service, err := NewOrganizedWithCapabilities(authority, state, semantic, embedding.HashForTesting{Dimensions: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	target, err := service.Create(context.Background(), CreateInput{Kind: domain.KindKnowledge, Content: "trusted target"})
	if err != nil {
		t.Fatal(err)
	}
	semantic.attack = true
	source, err := service.Create(context.Background(), CreateInput{
		Kind: domain.KindKnowledge, Content: "trusted source",
		Relations: []domain.ExplicitRelation{{Type: "related_to", TargetID: target.Information.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.Organization.Status != "pending" {
		t.Fatalf("untrusted result did not use safe degradation: %#v", source.Organization)
	}
	record, exists := state.Get(source.Information.ID)
	if !exists || record.Status != "pending" || record.Analysis.Summary != "" || len(record.Analysis.Relations) != 1 ||
		record.Analysis.Relations[0].Type != "related_to" || record.Analysis.Relations[0].TargetID != target.Information.ID {
		t.Fatalf("untrusted analysis reached derived organization: %#v", record)
	}
	authoritative, err := service.Read(context.Background(), source.Information.ID)
	if err != nil || authoritative.Content != "trusted source" || len(authoritative.Relations) != 1 || authoritative.Relations[0].TargetID != target.Information.ID {
		t.Fatalf("semantic result changed authoritative asset: %#v %v", authoritative, err)
	}
	if _, exists := authority.ReadCurrent("forged-target"); exists {
		t.Fatal("semantic result created an authoritative target")
	}
}
