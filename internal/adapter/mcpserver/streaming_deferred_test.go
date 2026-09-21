package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/semantics"
)

type countedDeferredVector struct {
	streamTestVector
	calls atomic.Int32
}

func (v *countedDeferredVector) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	v.calls.Add(1)
	return v.streamTestVector.EmbedDocuments(ctx, texts)
}

func TestDeferredStorageFormalToolLifecycle(t *testing.T) {
	v := &countedDeferredVector{}
	f := newStreamReplayFixture(t, v)
	foundJobs := false
	for tool, err := range f.session.Tools(f.ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		foundJobs = foundJobs || tool.Name == "ownward_semantic_jobs"
	}
	if !foundJobs {
		t.Fatal("organization tools missing from paginated contract")
	}
	create := map[string]any{"content": "项目星河负责人是李明。", "organization_mode": contract.DeferredOrganizationV1}
	var made, replay CreateOutput
	f.decode(f.call("ownward_create", "deferred", create), &made)
	f.decode(f.call("ownward_create", "deferred", create), &replay)
	id := made.Result.Information.ID
	if id != replay.Result.Information.ID || v.calls.Load() != 0 || made.Result.Organization.RequiredAction != "ownward_semantic_jobs" {
		t.Fatalf("not a deferred durable receipt: %+v calls=%d", made, v.calls.Load())
	}
	var raw ReadOutput
	f.decode(f.call("ownward_read", "", map[string]any{"id": id}), &raw)
	if raw.Information.Content != create["content"] {
		t.Fatal("original changed")
	}
	var search SearchOutput
	f.decode(f.call("ownward_search", "", map[string]any{"query": "星河"}), &search)
	if len(search.Results) == 0 || search.Results[0].ID != id {
		t.Fatal("raw lexical search unavailable", search)
	}
	if search.PendingOrganization == nil || search.PendingOrganization.Total != 1 || len(search.PendingOrganization.Assets) != 1 {
		t.Fatalf("pending organization signal missing: %+v", search.PendingOrganization)
	}
	pendingAsset := search.PendingOrganization.Assets[0]
	if pendingAsset.ID != id || pendingAsset.Status != "pending" || pendingAsset.RequiredAction != "ownward_semantic_jobs" || !strings.Contains(pendingAsset.Excerpt, "星河") || pendingAsset.Revision != 1 {
		t.Fatalf("pending signal fields wrong: %+v", pendingAsset)
	}
	var legacy SemanticWorkOutput
	f.decode(f.call("ownward_semantic_work", "", map[string]any{}), &legacy)
	if len(legacy.Work) != 0 {
		t.Fatal("legacy executor mixed with deferred work")
	}
	if !f.call("ownward_semantic_work", "", map[string]any{"asset_ids": []string{id}}).IsError {
		t.Fatal("unclaimed work disclosed")
	}
	var claimed, retry, busy contract.OrganizationJobResult
	args := map[string]any{"action": "claim", "request_id": "first", "asset_id": id}
	f.decode(f.call("ownward_semantic_jobs", "", args), &claimed)
	f.decode(f.call("ownward_semantic_jobs", "", args), &retry)
	if claimed.Claim == nil || retry.Claim == nil || claimed.Claim.Lease != retry.Claim.Lease {
		t.Fatal("claim retry lost execution identity")
	}
	f.decode(f.call("ownward_semantic_jobs", "", map[string]any{"action": "claim", "request_id": "second", "asset_id": id}), &busy)
	if busy.Claim != nil {
		t.Fatal("duplicate executor acquired work")
	}
	var work SemanticWorkOutput
	f.decode(f.call("ownward_semantic_work", "", map[string]any{"asset_ids": []string{id}, "lease": claimed.Claim.Lease}), &work)
	if len(work.Work) != 1 || work.Work[0].Asset.Content != raw.Information.Content {
		t.Fatal("claimed work missing", work)
	}
	var pendingStatus StatusOutput
	f.decode(f.call("ownward_status", "", map[string]any{"id": id}), &pendingStatus)
	if pendingStatus.Organization.RequiredAction != "ownward_semantic_jobs" {
		t.Fatal("prepared deferred work directs host to legacy protocol", pendingStatus)
	}
	old := claimed.Claim.Lease
	var released contract.OrganizationJobResult
	f.decode(f.call("ownward_semantic_jobs", "", map[string]any{"action": "release", "asset_id": id, "lease": old}), &released)
	f.decode(f.call("ownward_semantic_jobs", "", map[string]any{"action": "claim", "request_id": "replacement", "asset_id": id}), &claimed)
	if claimed.Claim == nil || claimed.Claim.Lease == old {
		t.Fatal("released identity reused")
	}
	w := work.Work[0]
	sub := semantics.Submission{Schema: semantics.SubmissionSchema, WorkID: w.ID, AssetID: id, Revision: 1, ExecutionLease: old, Capability: semantics.Capability{ID: "synthetic", Version: "1"}, Status: semantics.SubmissionComplete, Analysis: semantics.Analysis{Summary: "项目星河负责人是李明。"}}
	if !f.call("ownward_semantic_submit", "", map[string]any{"submission": sub}).IsError {
		t.Fatal("late executor published")
	}
	sub.ExecutionLease = ""
	if !f.call("ownward_semantic_submit", "", map[string]any{"submission": sub}).IsError {
		t.Fatal("legacy submit bypassed lease")
	}
	sub.ExecutionLease = claimed.Claim.Lease
	var accepted SemanticSubmitOutput
	f.decode(f.call("ownward_semantic_submit", "", map[string]any{"submission": sub}), &accepted)
	if !f.call("ownward_semantic_jobs", "", map[string]any{"action": "renew", "asset_id": id, "lease": sub.ExecutionLease}).IsError {
		t.Fatal("completed execution renewed")
	}
	if !f.call("ownward_semantic_work", "", map[string]any{"asset_ids": []string{id}, "lease": sub.ExecutionLease}).IsError {
		t.Fatal("completed execution restarted")
	}
	altered := sub
	altered.Analysis.Summary = "不同的提交"
	if !f.call("ownward_semantic_submit", "", map[string]any{"submission": altered}).IsError {
		t.Fatal("completed receipt replay accepted a different result")
	}
	if accepted.Organization.Status != "ready" {
		t.Fatal("organization did not complete", accepted)
	}
	beforeReplay := v.calls.Load()
	f.decode(f.call("ownward_semantic_submit", "", map[string]any{"submission": sub}), &accepted)
	if v.calls.Load() != beforeReplay {
		t.Fatal("completed receipt replay repeated vector preparation")
	}
	var available contract.OrganizationJobResult
	f.decode(f.call("ownward_semantic_jobs", "", map[string]any{"action": "wait"}), &available)
	if available.Available {
		t.Fatal("completed job remains in queue")
	}
	var settled SearchOutput
	f.decode(f.call("ownward_search", "", map[string]any{"query": "星河"}), &settled)
	if settled.PendingOrganization == nil || settled.PendingOrganization.Total != 0 || len(settled.PendingOrganization.Assets) != 0 {
		t.Fatalf("organized asset still pending: %+v", settled.PendingOrganization)
	}
	var updated UpdateOutput
	f.decode(f.call("ownward_update", "correct", map[string]any{"id": id, "expected_revision": 1, "content": "项目星河负责人已改为张华。", "organization_mode": contract.DeferredOrganizationV1}), &updated)
	if !f.call("ownward_semantic_submit", "", map[string]any{"submission": sub}).IsError {
		t.Fatal("old revision lease accepted")
	}
	f.decode(f.call("ownward_read", "", map[string]any{"id": id}), &raw)
	if !strings.Contains(raw.Information.Content, "张华") {
		t.Fatal("correction not directly readable")
	}
	var repending SearchOutput
	f.decode(f.call("ownward_search", "", map[string]any{"query": "星河"}), &repending)
	if repending.PendingOrganization == nil || repending.PendingOrganization.Total != 1 || repending.PendingOrganization.Assets[0].Revision != 2 {
		t.Fatalf("updated asset must return to pending: %+v", repending.PendingOrganization)
	}
}

func TestPendingOrganizationPagingIsBounded(t *testing.T) {
	v := &countedDeferredVector{}
	f := newStreamReplayFixture(t, v)
	for i, content := range []string{"星河项目记录一。", "星河项目记录二。", "星河项目记录三。"} {
		var made CreateOutput
		f.decode(f.call("ownward_create", fmt.Sprintf("pending-%d", i), map[string]any{"content": content, "organization_mode": contract.DeferredOrganizationV1}), &made)
	}
	var first SearchOutput
	f.decode(f.call("ownward_search", "", map[string]any{"query": "星河"}), &first)
	if first.PendingOrganization == nil || first.PendingOrganization.Total != 3 || len(first.PendingOrganization.Assets) != 3 {
		t.Fatalf("enumerable pending set wrong: %+v", first.PendingOrganization)
	}
	for _, asset := range first.PendingOrganization.Assets {
		if asset.Status != "pending" || asset.RequiredAction != "ownward_semantic_jobs" || asset.Excerpt == "" {
			t.Fatalf("pending entry incomplete: %+v", asset)
		}
	}
	var second SearchOutput
	f.decode(f.call("ownward_search", "", map[string]any{"query": "星河", "organization_offset": 2}), &second)
	if second.PendingOrganization == nil || second.PendingOrganization.Total != 3 || len(second.PendingOrganization.Assets) != 1 {
		t.Fatalf("pending paging wrong: %+v", second.PendingOrganization)
	}
	skipped := map[string]bool{first.PendingOrganization.Assets[0].ID: true, first.PendingOrganization.Assets[1].ID: true}
	if skipped[second.PendingOrganization.Assets[0].ID] {
		t.Fatal("offset must skip the first page")
	}
	var beyond SearchOutput
	f.decode(f.call("ownward_search", "", map[string]any{"query": "星河", "organization_offset": 10}), &beyond)
	if beyond.PendingOrganization == nil || beyond.PendingOrganization.Total != 3 || len(beyond.PendingOrganization.Assets) != 0 {
		t.Fatalf("offset beyond the window must return an empty page with stable total: %+v", beyond.PendingOrganization)
	}
}

func TestDeferredBatchKeepsDefaultPreparation(t *testing.T) {
	v := &countedDeferredVector{}
	f := newStreamReplayFixture(t, v)
	var out CreateBatchOutput
	f.decode(f.call("ownward_create_batch", "mixed", map[string]any{"items": []any{
		map[string]any{"content": "延后材料。", "organization_mode": contract.DeferredOrganizationV1},
		map[string]any{"content": "默认材料。"},
	}}), &out)
	if len(out.Results) != 2 || v.calls.Load() != 1 {
		t.Fatalf("mixed batch preparation: %+v calls=%d", out, v.calls.Load())
	}
	if out.Results[0].Result.Organization.RequiredAction != "ownward_semantic_jobs" || out.Results[1].Result.Organization.RequiredAction != "ownward_semantic_work" {
		t.Fatal("batch modes lost", out)
	}
	var pending SemanticWorkOutput
	f.decode(f.call("ownward_semantic_work", "", map[string]any{"limit": 20}), &pending)
	if len(pending.Work) != 1 || pending.Work[0].Asset.ID != out.Results[1].Result.Information.ID {
		t.Fatal("default work changed")
	}
	// 非 deferred 的未组织资产同样出现在待组织信号中，且 required_action 指向原协议。
	var pendingSignal SearchOutput
	f.decode(f.call("ownward_search", "", map[string]any{"query": "默认"}), &pendingSignal)
	if pendingSignal.PendingOrganization == nil || pendingSignal.PendingOrganization.Total != 1 {
		t.Fatalf("pending signal missing for default work: %+v", pendingSignal.PendingOrganization)
	}
	if entry := pendingSignal.PendingOrganization.Assets[0]; entry.RequiredAction != "ownward_semantic_work" || entry.ID != out.Results[1].Result.Information.ID {
		t.Fatalf("default pending entry wrong: %+v", entry)
	}
}
