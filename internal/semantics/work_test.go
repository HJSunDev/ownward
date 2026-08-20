package semantics

import (
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
)

func TestSemanticSubmissionIsBoundToWorkAndEvidence(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	asset := domain.Information{
		Schema: domain.AssetSchema, ID: "asset-current", Revision: 2,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, Kind: domain.KindGeneral, Content: "React 的状态更新会触发重新渲染。",
	}
	work, err := NewWork("generation-1", asset, []Candidate{{
		ID: "asset-related", Revision: 4, Content: "React 通过状态描述界面。",
	}}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	submission := Submission{
		Schema: SubmissionSchema, WorkID: work.ID, AssetID: asset.ID, Revision: asset.Revision,
		Capability: Capability{ID: "codex", Version: "gpt-5.4"}, Status: SubmissionComplete,
		Analysis: Analysis{Summary: "React 状态更新与渲染", Relations: []Relation{{
			Type: "related_to", TargetID: "asset-related", Direction: "outgoing", Confidence: 0.9, Evidence: "两项信息都明确讨论 React 状态与界面更新。",
		}}},
	}
	normalized, err := NormalizeSubmission(work, submission, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Analysis.Relations[0].TargetRevision != 4 || normalized.Analysis.Relations[0].InferredBy != asset.ID {
		t.Fatalf("relation provenance was not bound by the kernel: %#v", normalized.Analysis.Relations[0])
	}

	invalid := submission
	invalid.Analysis.Relations[0].TargetID = "not-in-work"
	if _, err := NormalizeSubmission(work, invalid, now); err == nil {
		t.Fatal("submission invented a relation target outside the bounded work")
	}
}

func TestSemanticSubmissionCanPreserveUncertainty(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	asset := domain.Information{
		Schema: domain.AssetSchema, ID: "asset", Revision: 1,
		CreatedAt: now, UpdatedAt: now, Kind: domain.KindGeneral, Content: "这周找个时间再看。",
	}
	work, err := NewWork("generation-1", asset, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	value, err := NormalizeSubmission(work, Submission{
		Schema: SubmissionSchema, WorkID: work.ID, AssetID: asset.ID, Revision: 1,
		Capability: Capability{ID: "agent", Version: "1"}, Status: SubmissionUncertain,
		Uncertainty: "缺少“这周”和事项所指的上下文。",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != SubmissionUncertain || value.Uncertainty == "" {
		t.Fatalf("uncertainty was lost: %#v", value)
	}
}
