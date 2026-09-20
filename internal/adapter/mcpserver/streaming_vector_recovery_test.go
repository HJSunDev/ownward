package mcpserver

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/semantics"
)

type recoveringDeferredVector struct {
	streamTestVector
	fail  atomic.Bool
	calls atomic.Int32
}

func (v *recoveringDeferredVector) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	v.calls.Add(1)
	if v.fail.Load() {
		return nil, errors.New("controlled temporary vector failure")
	}
	return v.streamTestVector.EmbedDocuments(ctx, texts)
}

func TestTemporaryVectorFailureRecovery(t *testing.T) {
	for _, scenario := range []struct{ mode, recovery string }{{"", ""}, {contract.DeferredOrganizationV1, ""}, {contract.DeferredOrganizationV1, "release"}, {contract.DeferredOrganizationV1, "expiry"}} {
		mode := scenario.mode
		name := mode + scenario.recovery
		if name == "" {
			name = "default_control"
		}
		t.Run(name, func(t *testing.T) {
			vectors := &recoveringDeferredVector{}
			vectors.fail.Store(true)
			f := newStreamReplayFixture(t, vectors)
			body := strings.Repeat("项目星河的负责人与会议记录保存在这份资料中。", 40)
			args := map[string]any{"content": body}
			if mode != "" {
				args["organization_mode"] = mode
			}
			var made CreateOutput
			f.decode(f.call("ownward_create", "initial-save", args), &made)
			id := made.Result.Information.ID
			lease := ""
			workArgs := map[string]any{"asset_ids": []string{id}}
			if mode != "" {
				var claim contract.OrganizationJobResult
				claimArgs := map[string]any{"action": "claim", "asset_id": id, "request_id": "first"}
				if scenario.recovery == "expiry" {
					claimArgs["lease_seconds"] = 1
				}
				f.decode(f.call("ownward_semantic_jobs", "", claimArgs), &claim)
				if claim.Claim == nil {
					t.Fatal("work not claimable")
				}
				lease = claim.Claim.Lease
				workArgs["lease"] = lease
			}
			var work SemanticWorkOutput
			f.decode(f.call("ownward_semantic_work", "", workArgs), &work)
			if len(work.Work) != 1 {
				t.Fatal("missing semantic work")
			}
			sub := semantics.Submission{Schema: semantics.SubmissionSchema, WorkID: work.Work[0].ID, AssetID: id, Revision: 1, ExecutionLease: lease, Capability: semantics.Capability{ID: "synthetic", Version: "1"}, Status: semantics.SubmissionComplete, Analysis: semantics.Analysis{Summary: "项目星河的负责人与会议记录。"}}
			var first, recovered SemanticSubmitOutput
			f.decode(f.call("ownward_semantic_submit", "", map[string]any{"submission": sub}), &first)
			if first.Organization.Status != "pending" {
				t.Fatalf("controlled failure did not reach pending: %+v", first)
			}
			firstCalls := vectors.calls.Load()
			generation, _, err := f.store.Generation(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			before, err := f.store.CurrentOrganization(f.ctx, generation, id)
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := f.store.RecordHeader(f.ctx, before)
			if err != nil {
				t.Fatal(err)
			}
			vectors.fail.Store(false)
			if scenario.recovery == "" {
				f.decode(f.call("ownward_semantic_submit", "", map[string]any{"submission": sub}), &recovered)
			} else {
				var job contract.OrganizationJobResult
				if scenario.recovery == "release" {
					f.decode(f.call("ownward_semantic_jobs", "", map[string]any{"action": "release", "asset_id": id, "lease": lease}), &job)
				} else {
					f.decode(f.call("ownward_semantic_jobs", "", map[string]any{"action": "wait", "asset_id": id, "wait_seconds": 2}), &job)
					if !job.Available {
						t.Fatal("expired incomplete work unavailable")
					}
				}
				f.decode(f.call("ownward_semantic_jobs", "", map[string]any{"action": "claim", "asset_id": id, "request_id": "resume"}), &job)
				if job.Claim == nil {
					t.Fatal("partial work lost its durable queue entry")
				}
				if !f.call("ownward_semantic_submit", "", map[string]any{"submission": sub}).IsError {
					t.Fatal("old worker survived reclaim")
				}
				f.decode(f.call("ownward_semantic_work", "", map[string]any{"asset_ids": []string{id}, "lease": job.Claim.Lease}), &work)
				if len(work.Work) != 0 {
					t.Fatal("accepted semantics dispatched to AI again")
				}
				var state StatusOutput
				f.decode(f.call("ownward_status", "", map[string]any{"id": id}), &state)
				recovered.Organization = state.Organization
				after, err := f.store.CurrentOrganization(f.ctx, generation, id)
				if err != nil {
					t.Fatal(err)
				}
				resumed, err := f.store.RecordHeader(f.ctx, after)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(accepted.SemanticReceipt, resumed.SemanticReceipt) || !reflect.DeepEqual(accepted.Analysis, resumed.Analysis) || !reflect.DeepEqual(accepted.InputAssets, resumed.InputAssets) {
					t.Fatal("vector recovery altered accepted semantic data")
				}
			}
			var pending contract.OrganizationJobResult
			f.decode(f.call("ownward_semantic_jobs", "", map[string]any{"action": "claim", "asset_id": id, "request_id": "recovery"}), &pending)
			var status StatusOutput
			f.decode(f.call("ownward_status", "", map[string]any{"id": id}), &status)
			var raw ReadOutput
			f.decode(f.call("ownward_read", "", map[string]any{"id": id}), &raw)
			if raw.Information.Content != body {
				t.Fatal("raw save lost")
			}
			t.Logf("mode=%q first=%s replay=%s status=%+v vector_calls=%d->%d recovery_claim=%v raw_preserved=true", mode, first.Organization.Status, recovered.Organization.Status, status.Organization, firstCalls, vectors.calls.Load(), pending.Claim != nil)
			if recovered.Organization.Status != "ready" {
				t.Fatal("accepted semantic result cannot complete after the temporary vector failure has recovered")
			}
			if vectors.calls.Load() != firstCalls+1 || pending.Claim != nil {
				t.Fatal("unexpected repeated vector work or unfinished queue")
			}
		})
	}
}
