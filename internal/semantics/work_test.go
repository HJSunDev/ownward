package semantics

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/domain"
)

func TestWorkReferenceRestoresExactPublicPayloadAndReceiptIdentity(t *testing.T) {
	now := time.Date(2026, 8, 27, 9, 30, 0, 0, time.UTC)
	asset := domain.Information{
		Schema: domain.AssetSchema, ID: "asset-current", Revision: 3,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, Kind: domain.KindKnowledge,
		Content: "authoritative current content " + strings.Repeat("x", 4096),
	}
	candidateAsset := domain.Information{
		Schema: domain.AssetSchema, ID: "asset-candidate", Revision: 7,
		CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-time.Minute), Kind: domain.KindKnowledge,
		Content: "authoritative candidate content " + strings.Repeat("y", 4096),
	}
	work, err := NewWork("generation-compact", asset, []Candidate{{
		ID: candidateAsset.ID, Revision: candidateAsset.Revision, Content: candidateAsset.Content, Similarity: 0.875,
	}}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := ReferenceWork(work)
	if err != nil {
		t.Fatal(err)
	}
	encodedReference, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedReference), asset.Content) || strings.Contains(string(encodedReference), candidateAsset.Content) {
		t.Fatal("compact work reference duplicated authoritative content")
	}
	restored, err := ResolveWork(reference, asset, []domain.Information{candidateAsset})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, work) {
		t.Fatalf("restored public work changed: got=%#v want=%#v", restored, work)
	}
	submission, err := NormalizeSubmissionReference(reference, asset, Submission{
		Schema: SubmissionSchema, WorkID: work.ID, AssetID: asset.ID, Revision: asset.Revision,
		Capability: Capability{ID: "codex", Version: "gpt-5.6-luna", Execution: "isolated"},
		Status:     SubmissionComplete, Analysis: Analysis{Summary: "compact semantic result"},
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := NewSubmissionReceipt(submission)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Matches(submission) {
		t.Fatal("compact receipt did not recognize the accepted submission")
	}
	changed := submission
	changed.Analysis.Summary = "different result"
	if receipt.Matches(changed) {
		t.Fatal("compact receipt accepted a semantically different submission")
	}
}

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
