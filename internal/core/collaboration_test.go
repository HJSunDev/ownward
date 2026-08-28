package core

import (
	"context"
	"errors"
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
	service, err := newTestCollaborative(t, assets, state, embedder)
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
	reopened, err := newTestCollaborative(t, assets, state, embedding.HashForTesting{Dimensions: 64})
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
	service, err := newTestCollaborative(t, assets, state, embedder)
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
	service, err := newTestCollaborative(t, assets, state, embedding.HashForTesting{Dimensions: 64})
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
	work := nextSemanticWork(t, ctx, service.Service)
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
	work = nextSemanticWork(t, ctx, service.Service)
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

func TestCollaborativeCompactReceiptPreservesIdempotencyAfterRestart(t *testing.T) {
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
	service, err := newTestCollaborative(t, assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateInput{Content: "A durable compact receipt keeps semantic submission retries idempotent."})
	if err != nil {
		t.Fatal(err)
	}
	work := nextSemanticWork(t, ctx, service.Service)
	submission := semanticSubmission(work, semantics.Analysis{Summary: "durable compact receipt"})
	if _, err := service.SubmitSemantic(ctx, submission); err != nil {
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
	reopened, err := newTestCollaborative(t, assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	organization, err := reopened.SubmitSemantic(ctx, submission)
	if err != nil || organization.Status != "ready" {
		t.Fatalf("identical semantic retry failed after restart: organization=%#v err=%v", organization, err)
	}
	stored, exists := state.Get(created.Information.ID)
	if !exists || stored.SemanticReceipt == nil || stored.SemanticWorkReference == nil {
		t.Fatalf("restarted state did not retain only compact semantic identity: %#v", stored)
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
	service, err := newTestCollaborative(t, assets, state, embedding.HashForTesting{Dimensions: 64})
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
	service, err := newTestCollaborative(t, assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	created, err := service.Create(ctx, CreateInput{Content: "用户每周一进行力量训练。", Kind: domain.KindGeneral})
	if err != nil {
		t.Fatal(err)
	}
	work := nextSemanticWork(t, ctx, service.Service)
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
	service, err := newTestCollaborative(t, assets, state, embedding.Unavailable{Reason: "expected failure"})
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
	service, err := newTestCollaborative(t, assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateInput{Content: "已完成组织的信息必须在向量能力临时不可用时保持有效。"})
	if err != nil {
		t.Fatal(err)
	}
	work := nextSemanticWork(t, ctx, service.Service)
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
	service, err = newTestCollaborative(t, assets, state, embedding.Unavailable{Reason: "temporary outage"})
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
	service, err := newTestCollaborative(t, assets, state, embedding.HashForTesting{Dimensions: 64})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateInput{Content: "向量空间必须按能力世代隔离。"})
	if err != nil {
		t.Fatal(err)
	}
	work := nextSemanticWork(t, ctx, service.Service)
	if _, err := service.SubmitSemantic(ctx, semanticSubmission(work, semantics.Analysis{Summary: "向量空间隔离"})); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	assets, _ = assetlog.Open(filepath.Join(root, "assets"))
	state, _ = derived.Open(filepath.Join(root, "state"))
	service, err = newTestCollaborative(t, assets, state, alternateEmbedding{HashForTesting: embedding.HashForTesting{Dimensions: 64}})
	if err != nil {
		t.Fatal(err)
	}
	organization, err := service.Organization(created.Information.ID)
	if err != nil || organization.Status != "pending" {
		t.Fatalf("changed vector space remained visible: state=%#v err=%v", organization, err)
	}
	if record, exists := state.GetWithEmbedding(created.Information.ID); !exists || len(record.Embedding) != 0 || !record.HasSemanticResult() {
		t.Fatalf("space isolation was not durable or discarded independent semantics: %#v", record)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	assets, _ = assetlog.Open(filepath.Join(root, "assets"))
	state, _ = derived.Open(filepath.Join(root, "state"))
	service, err = newTestCollaborative(t, assets, state, embedding.HashForTesting{Dimensions: 64})
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
	service, err := newTestCollaborative(t, assets, state, embedding.HashForTesting{Dimensions: 64})
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
	service, err := newTestCollaborative(t, assets, state, embedder)
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

type constantEmbedding struct{}

func (constantEmbedding) Name() string { return "constant-batch-test" }
func (constantEmbedding) Space() embedding.Space {
	return embedding.Space{ID: "constant-batch-test-v1", Dimensions: 2}
}
func (constantEmbedding) EmbedDocuments(_ context.Context, values []string) ([][]float32, error) {
	result := make([][]float32, len(values))
	for index := range result {
		result[index] = []float32{1, 0}
	}
	return result, nil
}
func (constantEmbedding) EmbedQuery(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}
func (constantEmbedding) Close() error { return nil }

func TestCreateBatchPreservesEarlierBatchSemanticCandidatesBeforePublishing(t *testing.T) {
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
	service, err := newTestCollaborative(t, assets, state, constantEmbedding{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	created, err := service.CreateBatch(ctx, []CreateInput{
		{Content: "orchid-vault-only-token"},
		{Content: "quartz-harbor-only-token"},
	})
	if err != nil || len(created) != 2 || created[0].Result == nil || created[1].Result == nil {
		t.Fatalf("batch creation failed: %#v %v", created, err)
	}
	work, err := service.SemanticWorkFor(ctx, []string{created[1].Result.Information.ID})
	if err != nil || len(work) != 1 {
		t.Fatalf("second batch work missing: %#v %v", work, err)
	}
	found := false
	for _, candidate := range work[0].Candidates {
		if candidate.ID == created[0].Result.Information.ID && candidate.Similarity > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("durable batch changed predecessor semantic candidate visibility: %#v", work[0].Candidates)
	}
}

type boundedSemanticEmbedding struct {
	documentCalls [][]string
	failNext      bool
}

func (*boundedSemanticEmbedding) Name() string { return "bounded-semantic-test" }
func (*boundedSemanticEmbedding) Space() embedding.Space {
	return embedding.Space{ID: "bounded-semantic-test-v1", Dimensions: 2}
}
func (b *boundedSemanticEmbedding) EmbedDocuments(_ context.Context, values []string) ([][]float32, error) {
	b.documentCalls = append(b.documentCalls, append([]string(nil), values...))
	if b.failNext {
		b.failNext = false
		return nil, errors.New("synthetic bounded failure")
	}
	total := 0
	result := make([][]float32, len(values))
	for index, value := range values {
		total += len([]byte(value))
		if len([]byte(value)) > semanticEmbeddingChunkBytes {
			return nil, errors.New("input exceeds synthetic embedding window")
		}
		if strings.Contains(value, "cobalt lattice") {
			result[index] = []float32{1, 0}
		} else {
			result[index] = []float32{0, 1}
		}
	}
	if total > semanticEmbeddingChunkBytes {
		return nil, errors.New("batch exceeds synthetic embedding window")
	}
	return result, nil
}
func (*boundedSemanticEmbedding) EmbedQuery(_ context.Context, value string) ([]float32, error) {
	if strings.Contains(value, "极地成像") {
		return []float32{1, 0}, nil
	}
	return []float32{0, 1}, nil
}
func (*boundedSemanticEmbedding) Close() error { return nil }

func TestSemanticSubmissionCreatesOneRecoverableVectorForLongAssets(t *testing.T) {
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
	embedder := &boundedSemanticEmbedding{}
	service, err := newTestCollaborative(t, assets, state, embedder)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateBatch(ctx, []CreateInput{
		{Content: strings.Repeat("Record AX-41 retained procedure CL-9 after stability trials. ", 40)},
		{Content: strings.Repeat("Record BX-72 retained procedure MR-3 after optics trials. ", 40)},
	})
	if err != nil || len(created) != 2 {
		t.Fatalf("long batch creation failed: results=%#v err=%v", created, err)
	}
	if len(embedder.documentCalls) != 0 {
		t.Fatalf("oversized raw assets reached the bounded embedding transport: %#v", embedder.documentCalls)
	}
	ids := []string{created[0].Result.Information.ID, created[1].Result.Information.ID}
	work, err := service.SemanticWorkFor(ctx, ids)
	if err != nil || len(work) != 2 {
		t.Fatalf("long assets did not expose semantic work: work=%#v err=%v", work, err)
	}
	submissions := []semantics.Submission{
		semanticSubmission(work[0], semantics.Analysis{
			Summary: "The polar imaging drift was resolved by adopting a cobalt lattice method.",
			Cues:    []semantics.Cue{{Text: "cobalt lattice for polar imaging stability", Kind: "method"}},
			Topics:  []string{"polar imaging"},
		}),
		semanticSubmission(work[1], semantics.Analysis{
			Summary: "The harbor audio distortion was handled with a moss relay method.",
			Cues:    []semantics.Cue{{Text: "moss relay for harbor audio", Kind: "method"}},
			Topics:  []string{"harbor audio"},
		}),
	}
	results, err := service.SubmitSemanticBatch(ctx, submissions)
	if err != nil || len(results) != 2 || results[0].Organization.Status != "ready" || results[1].Organization.Status != "ready" {
		t.Fatalf("semantic representation recovery failed: results=%#v err=%v", results, err)
	}
	if len(embedder.documentCalls) != 1 || len(embedder.documentCalls[0]) != 2 {
		t.Fatalf("semantic submissions did not share the bounded embedding batch: %#v", embedder.documentCalls)
	}
	stored, err := state.AllWithEmbeddings()
	if err != nil || len(stored) != 2 {
		t.Fatalf("derived records are incomplete: records=%d err=%v", len(stored), err)
	}
	for _, record := range stored {
		if len(record.Embedding) != 2 {
			t.Fatalf("long asset did not retain exactly one vector: %#v", record)
		}
	}
	search, err := service.Search(ctx, SearchInput{Query: "极地成像漂移采用了哪种结构策略？", Limit: 2})
	if err != nil || len(search) == 0 || search[0].ID != ids[0] || !contains(search[0].Signals, "semantic") {
		t.Fatalf("public search did not consume the recovered semantic representation: results=%#v err=%v", search, err)
	}
	callsBeforeRebuild := len(embedder.documentCalls)
	counts, err := service.Maintain(ctx, true)
	if err != nil || counts["ready"] != 2 {
		t.Fatalf("rebuild did not preserve submitted semantic representations: counts=%#v err=%v", counts, err)
	}
	if len(embedder.documentCalls) != callsBeforeRebuild {
		t.Fatalf("rebuild repeated valid semantic embedding work: before=%d after=%d", callsBeforeRebuild, len(embedder.documentCalls))
	}
	rebuilt, err := service.Search(ctx, SearchInput{Query: "极地成像漂移采用了哪种结构策略？", Limit: 2})
	if err != nil || len(rebuilt) == 0 || rebuilt[0].ID != ids[0] || !contains(rebuilt[0].Signals, "semantic") {
		t.Fatalf("rebuild lost the semantic representation: results=%#v err=%v", rebuilt, err)
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
	reopened, err := newTestCollaborative(t, assets, state, &boundedSemanticEmbedding{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.Search(ctx, SearchInput{Query: "极地成像漂移采用了哪种结构策略？", Limit: 2})
	if err != nil || len(restored) == 0 || restored[0].ID != ids[0] || !contains(restored[0].Signals, "semantic") {
		t.Fatalf("reopened service lost the semantic representation: results=%#v err=%v", restored, err)
	}
}

func TestAcceptedSemanticSubmissionCanRecoverAPreviouslyFailedVector(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, _ := assetlog.Open(filepath.Join(root, "assets"))
	state, _ := derived.Open(filepath.Join(root, "state"))
	embedder := &boundedSemanticEmbedding{failNext: true}
	service, err := newTestCollaborative(t, assets, state, embedder)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	created, err := service.Create(ctx, CreateInput{Content: strings.Repeat("Record AX-41 retained procedure CL-9. ", 40)})
	if err != nil {
		t.Fatal(err)
	}
	work, err := service.SemanticWorkFor(ctx, []string{created.Information.ID})
	if err != nil || len(work) != 1 {
		t.Fatalf("semantic work missing: %#v %v", work, err)
	}
	submission := semanticSubmission(work[0], semantics.Analysis{Summary: "cobalt lattice method for polar imaging"})
	first, err := service.SubmitSemantic(ctx, submission)
	if err != nil || first.Status != "pending" {
		t.Fatalf("failed recovery did not preserve accepted pending state: %#v %v", first, err)
	}
	second, err := service.SubmitSemantic(ctx, submission)
	if err != nil || second.Status != "ready" {
		t.Fatalf("identical accepted submission did not recover its vector: %#v %v", second, err)
	}
}

func TestSemanticEmbeddingChunksPreserveAllNormalizedFieldsWithinTransportBound(t *testing.T) {
	analysis := semantics.Analysis{
		Summary:  strings.Repeat("summary-field ", 40),
		Topics:   []string{"topic-field"},
		Cues:     []semantics.Cue{{Text: "cue-field", Kind: "entity"}},
		Contexts: []semantics.InferredContext{{Key: "context-key", Value: "context-value", Evidence: "context-evidence"}},
		Relations: []semantics.Relation{{
			Type: "supports", Direction: "outgoing", TargetID: "target-field", Evidence: "relation-evidence",
		}},
	}
	chunks := semanticEmbeddingChunks(analysis)
	joined := strings.Join(chunks, "\n")
	for _, expected := range []string{"summary-field", "topic-field", "cue-field", "context-key", "context-evidence", "target-field", "relation-evidence"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("semantic field %q was omitted from retrieval representation", expected)
		}
	}
	for _, chunk := range chunks {
		if len([]byte(chunk)) > semanticEmbeddingChunkBytes {
			t.Fatalf("semantic chunk exceeded transport bound: %d", len([]byte(chunk)))
		}
	}
}

func TestBoundedEmbeddingBatchesPreserveOrderAndTotalTransportBound(t *testing.T) {
	values := []string{strings.Repeat("a", 170), strings.Repeat("b", 150), strings.Repeat("c", 170)}
	offset := 0
	for offset < len(values) {
		end := boundedEmbeddingBatchEnd(values, offset)
		if end <= offset {
			t.Fatal("bounded embedding batch made no progress")
		}
		total := 0
		for _, value := range values[offset:end] {
			total += len([]byte(value))
		}
		if total > semanticEmbeddingChunkBytes {
			t.Fatalf("embedding batch exceeded total transport bound: %d", total)
		}
		offset = end
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
