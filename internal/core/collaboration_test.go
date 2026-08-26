package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
)

func TestCollaborativeLongAssetUsesTraceableEvidenceUnits(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	embedder := &countingEmbedding{HashForTesting: embedding.HashForTesting{Dimensions: 64}}
	service, err := NewCollaborative(assets, state, embedder)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateInput{Content: "astral compass identifies the safe route. " + strings.Repeat("unrelated archive padding. ", 80)})
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.Search(ctx, SearchInput{Query: "astral compass", Limit: 5})
	if err != nil || len(results) == 0 || results[0].ID != created.Information.ID {
		t.Fatalf("long asset did not return source-bound evidence: results=%#v err=%v", results, err)
	}
	references, err := service.SearchEvidence(ctx, EvidenceSearchInput{SourceID: results[0].ID, Query: "astral compass", Limit: 3})
	if err != nil || len(references) == 0 {
		t.Fatalf("long asset evidence search failed: evidence=%#v err=%v", references, err)
	}
	reference := references[0]
	evidence, err := service.ReadEvidence(ctx, reference.ID)
	if err != nil || evidence.Reference() != reference || evidence.SourceID != created.Information.ID || !strings.Contains(evidence.Content, "astral compass") {
		t.Fatalf("evidence did not resolve to the authoritative source: evidence=%#v err=%v", evidence, err)
	}
	if _, err := service.Read(ctx, reference.ID); err == nil {
		t.Fatal("derived evidence identity must not masquerade as an authoritative asset")
	}
	updatedContent := "updated beacon identifies the safe route. " + strings.Repeat("new archive padding. ", 80)
	updated, err := service.Update(ctx, UpdateInput{ID: created.Information.ID, ExpectedRevision: 1, Content: &updatedContent})
	if err != nil || updated.Information.Revision != 2 {
		t.Fatalf("update failed: result=%#v err=%v", updated, err)
	}
	if _, err := service.ReadEvidence(ctx, reference.ID); err == nil {
		t.Fatal("old evidence reference survived a source revision change")
	}
	updatedResults, err := service.Search(ctx, SearchInput{Query: "updated beacon", Limit: 5})
	if err != nil || len(updatedResults) == 0 {
		t.Fatalf("updated source search failed: results=%#v err=%v", updatedResults, err)
	}
	updatedEvidence, err := service.SearchEvidence(ctx, EvidenceSearchInput{SourceID: updatedResults[0].ID, Query: "updated beacon", Limit: 3})
	if err != nil || len(updatedEvidence) == 0 || updatedEvidence[0].SourceRevision != 2 {
		t.Fatalf("updated source did not expose new evidence: evidence=%#v err=%v", updatedEvidence, err)
	}
	for _, count := range embedder.calls {
		if count != 1 {
			t.Fatalf("fine-grained evidence multiplied organization embeddings: calls=%v", embedder.calls)
		}
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	assets, err = assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err = derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := NewCollaborative(assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.ReadEvidence(ctx, updatedEvidence[0].ID)
	if err != nil || restored.SourceRevision != 2 || !strings.Contains(restored.Content, "updated beacon") {
		t.Fatalf("self-contained evidence reference did not restore after reopen: evidence=%#v err=%v", restored, err)
	}
}

type countingEmbedding struct {
	embedding.HashForTesting
	calls      []int
	queryCalls int
}

type alternateEmbedding struct{ embedding.HashForTesting }

func (a alternateEmbedding) Space() embedding.Space {
	return embedding.Space{ID: "alternate-test-space", Dimensions: a.HashForTesting.Space().Dimensions}
}

func (c *countingEmbedding) EmbedDocuments(ctx context.Context, values []string) ([][]float32, error) {
	c.calls = append(c.calls, len(values))
	return c.HashForTesting.EmbedDocuments(ctx, values)
}

func (c *countingEmbedding) EmbedQuery(ctx context.Context, value string) ([]float32, error) {
	c.queryCalls++
	return c.HashForTesting.EmbedQuery(ctx, value)
}

func TestCollaborativeSearchSkipsEmbeddingWithoutComparableVectors(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	embedder := &countingEmbedding{HashForTesting: embedding.HashForTesting{Dimensions: 64}}
	service, err := NewCollaborative(assets, state, embedder)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	results, err := service.Search(ctx, SearchInput{Query: "尚不存在的信息"})
	if err != nil || len(results) != 0 {
		t.Fatalf("unexpected empty search: results=%#v err=%v", results, err)
	}
	if embedder.queryCalls != 0 {
		t.Fatalf("empty vector index triggered %d unnecessary query embeddings", embedder.queryCalls)
	}
}

func TestCollaborativeSemanticWorkIsVersionedAndAppliedByTheKernel(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCollaborative(assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	first, err := service.Create(ctx, CreateInput{Content: "React 使用状态描述会随交互变化的界面。", Kind: domain.KindGeneral})
	if err != nil {
		t.Fatal(err)
	}
	if first.Organization.Status != "pending" || first.Organization.RequiredAction != semanticWorkRequiredAction {
		t.Fatalf("asset must be durable before external understanding completes: %#v", first.Organization)
	}
	work := nextSemanticWork(t, ctx, service)
	if work.Asset.ID != first.Information.ID || len(work.Candidates) != 0 {
		t.Fatalf("unexpected first semantic work: %#v", work)
	}
	if _, err := service.SubmitSemantic(ctx, semanticSubmission(work, semantics.Analysis{Summary: "React 状态与界面"})); err != nil {
		t.Fatal(err)
	}

	second, err := service.Create(ctx, CreateInput{Content: "React 状态更新会触发相关界面重新渲染。", Kind: domain.KindGeneral})
	if err != nil {
		t.Fatal(err)
	}
	work = nextSemanticWork(t, ctx, service)
	if work.Asset.ID != second.Information.ID || len(work.Candidates) == 0 || work.Candidates[0].Revision == 0 {
		t.Fatalf("semantic work did not bind candidate revisions: %#v", work)
	}
	submission := semanticSubmission(work, semantics.Analysis{Summary: "React 状态更新与渲染", Relations: []semantics.Relation{{
		Type: "related_to", TargetID: first.Information.ID, Direction: "outgoing", Confidence: 0.94,
		Evidence: "两项资产都明确讨论 React 状态与界面更新。",
	}}})
	accepted, err := service.SubmitSemantic(ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != "ready" {
		t.Fatalf("complete semantics and vector must become ready: %#v", accepted)
	}
	if _, err := service.SubmitSemantic(ctx, submission); err != nil {
		t.Fatalf("identical retry must be idempotent: %v", err)
	}
	navigation, err := service.Navigate(ctx, []string{second.Information.ID}, nil, 1, 10)
	if err != nil || len(navigation.Edges) != 1 || navigation.Edges[0].TargetID != first.Information.ID {
		t.Fatalf("kernel did not materialize the accepted relation: %#v, %v", navigation, err)
	}

	changed := "React 状态更新后，框架会安排一次新的渲染。"
	updated, err := service.Update(ctx, UpdateInput{ID: second.Information.ID, ExpectedRevision: 1, Content: &changed})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SubmitSemantic(ctx, submission); err == nil {
		t.Fatal("a semantic result from an older asset revision was accepted")
	}
	if updated.Organization.Status != "pending" || updated.Organization.RequiredAction != semanticWorkRequiredAction {
		t.Fatalf("updated asset must expose pending understanding: %#v", updated.Organization)
	}
}

func TestUpdateRefreshesPendingSemanticWorkThatReferencesTheOldRevision(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCollaborative(assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	created, err := service.CreateBatch(ctx, []CreateInput{
		{Content: "The workshop meets in the old hall."},
		{Content: "The booking confirms the workshop venue."},
	})
	if err != nil || len(created) != 2 || created[0].Result == nil || created[1].Result == nil {
		t.Fatalf("create batch failed: results=%#v err=%v", created, err)
	}
	firstID := created[0].Result.Information.ID
	secondID := created[1].Result.Information.ID
	changed := "The workshop now meets in the glass annex."
	if _, err := service.Update(ctx, UpdateInput{ID: firstID, ExpectedRevision: 1, Content: &changed}); err != nil {
		t.Fatal(err)
	}
	work, err := service.SemanticWorkFor(ctx, []string{secondID})
	if err != nil || len(work) != 1 {
		t.Fatalf("dependent semantic work is unavailable: work=%#v err=%v", work, err)
	}
	foundCurrent := false
	for _, candidate := range work[0].Candidates {
		if candidate.ID == firstID {
			foundCurrent = candidate.Revision == 2 && candidate.Content == changed
		}
	}
	if !foundCurrent {
		t.Fatalf("pending semantic work retained an obsolete candidate: %#v", work[0].Candidates)
	}
}

func TestCollaborativeRebuildSwitchesOnlyACompleteGeneration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCollaborative(assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	created, err := service.Create(ctx, CreateInput{Content: "用户每周一进行力量训练。", Kind: domain.KindGeneral})
	if err != nil {
		t.Fatal(err)
	}
	work := nextSemanticWork(t, ctx, service)
	if _, err := service.SubmitSemantic(ctx, semanticSubmission(work, semantics.Analysis{Summary: "周一力量训练"})); err != nil {
		t.Fatal(err)
	}
	before := state.Generation()
	counts, err := service.Maintain(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Generation() == before || counts["ready"] != 1 {
		t.Fatalf("generation was not switched with preserved semantics: generation=%q counts=%#v", state.Generation(), counts)
	}
	if record, exists := state.GetWithEmbedding(created.Information.ID); !exists || len(record.Embedding) != 64 {
		t.Fatalf("switched generation lost its vector: record=%#v exists=%v", record, exists)
	}
	if workItems, err := service.SemanticWork(ctx, 1); err != nil || len(workItems) != 0 {
		t.Fatalf("accepted semantics became pending after rebuild: work=%#v err=%v", workItems, err)
	}
	results, err := service.Search(ctx, SearchInput{Query: "周一训练", Limit: 1})
	if err != nil || len(results) != 1 || results[0].ID != created.Information.ID {
		t.Fatalf("switched generation is not searchable: results=%#v err=%v", results, err)
	}
}

func TestCollaborativeRebuildKeepsCurrentGenerationWhenEmbeddingFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCollaborative(assets, state, embedding.Unavailable{Reason: "expected failure"})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	created, err := service.Create(ctx, CreateInput{Content: "即使向量不可用，资产仍然必须保存。", Kind: domain.KindGeneral})
	if err != nil {
		t.Fatal(err)
	}
	before := state.Generation()
	if _, err := service.Maintain(ctx, true); err == nil {
		t.Fatal("rebuild unexpectedly replaced an existing generation without embeddings")
	}
	if state.Generation() != before {
		t.Fatal("failed rebuild changed the current generation")
	}
	if value, err := service.Read(ctx, created.Information.ID); err != nil || value.Content != created.Information.Content {
		t.Fatalf("failed rebuild affected authoritative asset: value=%#v err=%v", value, err)
	}
}

func TestUnavailableCapabilityPreservesLastValidVectorsAndOrganization(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCollaborative(assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateInput{Content: "已完成组织的信息必须在向量能力临时不可用时保持有效。"})
	if err != nil {
		t.Fatal(err)
	}
	work := nextSemanticWork(t, ctx, service)
	if _, err := service.SubmitSemantic(ctx, semanticSubmission(work, semantics.Analysis{Summary: "保持最后有效状态"})); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	assets, err = assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err = derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err = NewCollaborative(assets, state, embedding.Unavailable{Reason: "temporary outage"})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	organization, err := service.Organization(created.Information.ID)
	if err != nil || organization.Status != "ready" {
		t.Fatalf("temporary capability outage invalidated organization: state=%#v err=%v", organization, err)
	}
	if record, exists := state.GetWithEmbedding(created.Information.ID); !exists || len(record.Embedding) != 64 {
		t.Fatalf("temporary capability outage discarded a valid vector: %#v", record)
	}
}

func TestChangedVectorSpaceIsPersistentlyIsolatedUntilRebuild(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCollaborative(assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateInput{Content: "向量空间必须按能力世代隔离。"})
	if err != nil {
		t.Fatal(err)
	}
	work := nextSemanticWork(t, ctx, service)
	if _, err := service.SubmitSemantic(ctx, semanticSubmission(work, semantics.Analysis{Summary: "向量空间隔离"})); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	assets, _ = assetlog.Open(filepath.Join(root, "assets"))
	state, _ = derived.Open(filepath.Join(root, "state"))
	service, err = NewCollaborative(assets, state, alternateEmbedding{HashForTesting: embedding.HashForTesting{Dimensions: 64}})
	if err != nil {
		t.Fatal(err)
	}
	organization, err := service.Organization(created.Information.ID)
	if err != nil || organization.Status != "pending" {
		t.Fatalf("changed vector space remained visible: state=%#v err=%v", organization, err)
	}
	if record, exists := state.GetWithEmbedding(created.Information.ID); !exists || len(record.Embedding) != 0 || record.SemanticResult == nil {
		t.Fatalf("space isolation was not durable or discarded independent semantics: %#v", record)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	assets, _ = assetlog.Open(filepath.Join(root, "assets"))
	state, _ = derived.Open(filepath.Join(root, "state"))
	service, err = NewCollaborative(assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	counts, err := service.Maintain(ctx, true)
	if err != nil || counts["ready"] != 1 {
		t.Fatalf("original capability did not recover through generation rebuild: counts=%#v err=%v", counts, err)
	}
	if work, err := service.SemanticWork(ctx, 1); err != nil || len(work) != 0 {
		t.Fatalf("vector recovery unnecessarily discarded accepted semantics: work=%#v err=%v", work, err)
	}
}

func TestSemanticBatchReportsEachIndependentResult(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCollaborative(assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for _, content := range []string{"用户偏好安静的工作环境。", "用户每周复盘一次工作方法。"} {
		if _, err := service.Create(ctx, CreateInput{Content: content}); err != nil {
			t.Fatal(err)
		}
	}
	work, err := service.SemanticWork(ctx, 2)
	if err != nil || len(work) != 2 {
		t.Fatalf("unexpected work: %#v, %v", work, err)
	}
	valid := semanticSubmission(work[0], semantics.Analysis{Summary: "安静工作环境偏好"})
	invalid := semanticSubmission(work[1], semantics.Analysis{Summary: "每周工作复盘"})
	invalid.Revision++
	results, err := service.SubmitSemanticBatch(ctx, []semantics.Submission{valid, invalid})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Error != "" || results[0].Organization.Status != "ready" || results[1].Error == "" {
		t.Fatalf("batch did not preserve independent outcomes: %#v", results)
	}
}

func TestCreateBatchSharesOneEmbeddingRequest(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	embedder := &countingEmbedding{HashForTesting: embedding.HashForTesting{Dimensions: 64}}
	service, err := NewCollaborative(assets, state, embedder)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	results, err := service.CreateBatch(ctx, []CreateInput{
		{Content: "第一项可长期复用的信息。"},
		{Content: "第二项可长期复用的信息。"},
		{Content: "第三项可长期复用的信息。"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || len(embedder.calls) != 1 || embedder.calls[0] != 3 {
		t.Fatalf("batch did not share one embedding request: results=%#v calls=%#v", results, embedder.calls)
	}
	for _, result := range results {
		if result.Error != "" || result.Result == nil || result.Result.Organization.Status != "pending" {
			t.Fatalf("unexpected independent batch result: %#v", result)
		}
	}
	work, err := service.SemanticWork(ctx, 20)
	if err != nil || len(work) != 3 {
		t.Fatalf("batch did not create semantic work: work=%#v err=%v", work, err)
	}
	targeted, err := service.SemanticWorkFor(ctx, []string{results[2].Result.Information.ID, results[0].Result.Information.ID})
	if err != nil || len(targeted) != 2 || targeted[0].Asset.ID != results[2].Result.Information.ID || targeted[1].Asset.ID != results[0].Result.Information.ID {
		t.Fatalf("targeted semantic work changed the requested partition: work=%#v err=%v", targeted, err)
	}
}

func nextSemanticWork(t *testing.T, ctx context.Context, service *Service) semantics.Work {
	t.Helper()
	work, err := service.SemanticWork(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 {
		t.Fatalf("expected one semantic work item, got %d", len(work))
	}
	return work[0]
}

func semanticSubmission(work semantics.Work, analysis semantics.Analysis) semantics.Submission {
	return semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: work.Asset.ID, Revision: work.Asset.Revision,
		Capability: semantics.Capability{ID: "test-agent", Version: "1"}, Status: semantics.SubmissionComplete, Analysis: analysis,
	}
}
