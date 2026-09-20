package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/codexplugin"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Actual official App Server, formal connector IO and authoritative kernel;
// only model output and vectors are deterministic local fixtures.
func TestOrganizationFormalConnectorContinueRestart(t *testing.T) {
	testOrganizationFormalConnector(t, false, false)
}

func TestOrganizationAcceptedSemanticResumesVectors(t *testing.T) {
	testOrganizationFormalConnector(t, true, false)
}

func TestOrganizationForegroundDependencyContinuesSameWork(t *testing.T) {
	testOrganizationFormalConnector(t, false, true)
}

func TestOrganizationForegroundDependencyResumesVectors(t *testing.T) {
	testOrganizationFormalConnector(t, true, true)
}

func TestOrganizationCompletedDemandYieldsToNextTask(t *testing.T) {
	testOrganizationFormalConnector(t, false, true, "stale")
}

func TestOrganizationExhaustionVisibleToForeground(t *testing.T) {
	testOrganizationFormalConnector(t, false, true, "exhausted")
}

func testOrganizationFormalConnector(t *testing.T, vectorFailure, demand bool, scenario ...string) {
	exe := os.Getenv("OWNWARD_TEST_CODEX_EXECUTABLE")
	if exe == "" {
		t.Skip("requires selected local Codex binary; no live inference")
	}
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	t.Setenv("no_proxy", "127.0.0.1,localhost")
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if demand {
		t.Setenv("OWNWARD_INFORMATION_USE_PATHS", "v1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	check := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	var modelCalls atomic.Int32
	started := make(chan string, 16)
	gate := make(chan struct{})
	var block atomic.Bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		if e := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body); e != nil {
			t.Error(e)
			return
		}
		modelCalls.Add(1)
		work, found := organizationFixtureWork(body)
		output := false
		var inspect func(any)
		inspect = func(v any) {
			switch v := v.(type) {
			case map[string]any:
				if v["type"] == "function_call_output" {
					output = true
				}
				for _, x := range v {
					inspect(x)
				}
			case []any:
				for _, x := range v {
					inspect(x)
				}
			}
		}
		inspect(body)
		item := map[string]any{"id": "msg", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Done", "annotations": []any{}}}}
		if !output {
			if !found {
				t.Error("model received no kernel work")
				http.Error(w, "missing", 400)
				return
			}
			started <- work.Asset.ID
			if block.Load() && strings.Contains(work.Asset.Content, "恢复后") {
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
			}
			sub := semantics.Submission{Schema: semantics.SubmissionSchema, WorkID: work.ID, AssetID: work.Asset.ID, Revision: work.Asset.Revision, Capability: semantics.Capability{ID: "local-fixture", Version: "1"}, Status: semantics.SubmissionComplete, Analysis: semantics.Analysis{Summary: work.Asset.Content}}
			if strings.Contains(work.Asset.Content, "FAIL") {
				sub.Revision = 0
			}
			args, _ := json.Marshal(map[string]any{"submission": sub})
			item = map[string]any{"id": "fc", "call_id": "call", "type": "function_call", "name": "ownward_semantic_submit", "arguments": string(args), "status": "completed"}
		}
		response := map[string]any{"id": fmt.Sprintf("resp_%d", modelCalls.Load()), "object": "response", "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 20, "output_tokens": 10, "total_tokens": 30, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, v := range []map[string]any{{"type": "response.created", "response": map[string]any{"id": response["id"], "status": "in_progress", "output": []any{}}}, {"type": "response.output_item.added", "output_index": 0, "item": item}, {"type": "response.output_item.done", "output_index": 0, "item": item}, {"type": "response.completed", "response": response}} {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", v["type"], b)
		}
	}))
	defer model.Close()
	home := t.TempDir()
	check(os.WriteFile(filepath.Join(home, "config.toml"), []byte(fmt.Sprintf("model=\"probe\"\nmodel_provider=\"probe\"\n[model_providers.probe]\nname=\"fixture\"\nbase_url=\"%s/v1\"\nwire_api=\"responses\"\nrequires_openai_auth=false\nrequest_max_retries=0\nstream_max_retries=0\n", model.URL)), 0600))
	profile := codexplugin.OrganizationProfile{Executable: exe, Home: home, Model: "probe", Provider: "probe", Effort: "low", Reservation: "isolated-local-fixture", MemoryMiB: 512, TimeoutSeconds: 20, MaxSubmissions: 2, MaxAttempts: 2, MaxTokens: 10000}
	profilePath := filepath.Join(t.TempDir(), "profile.json")
	data, _ := json.Marshal(profile)
	check(os.WriteFile(profilePath, data, 0600))
	t.Setenv("OWNWARD_ORGANIZATION_PROFILE", profilePath)
	root := t.TempDir()
	budget, e := resourcebudget.New(12*resourcebudget.MiB, resourcebudget.MiB)
	check(e)
	store, e := boundedstore.Open(ctx, filepath.Join(root, "ownward.sqlite"), boundedstore.Options{Budget: budget})
	check(e)
	defer store.Close()
	principal := contract.Principal{ID: "local", Revision: 1, CredentialDigest: strings.Repeat("a", 64), Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission}}
	check(store.PublishAccess(ctx, boundedstore.AccessHeader{System: "fixture", Revision: 1}, 0, []contract.Principal{principal}))
	vectors := &organizationOneVectorFailure{HashForTesting: embedding.HashForTesting{Dimensions: 512}}
	submitStates := make(chan string, 8)
	assets := &core.StreamingAssets{Store: store, Budget: budget, Scratch: root, DiskBytes: 64 * resourcebudget.MiB, Embedder: vectors}
	service := mcpserver.NewStreamingStorage(assets, "unit2", root, budget, 64*resourcebudget.MiB)
	var revoked atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case controlPrefix + "self":
			if revoked.Load() {
				w.WriteHeader(403)
				return
			}
			json.NewEncoder(w).Encode(principal)
		case controlPrefix + "generation":
			json.NewEncoder(w).Encode(map[string]any{"generation": 1})
		default:
			service.HTTPHandler().ServeHTTP(w, r.WithContext(contract.WithAuthenticationDigest(r.Context(), principal.CredentialDigest)))
		}
	}))
	defer api.Close()
	transport := &connectorTransport{base: http.DefaultTransport}
	upstream, e := mcp.NewClient(&mcp.Implementation{Name: "connector-fixture"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: api.URL, HTTPClient: &http.Client{Transport: transport}, DisableStandaloneSSE: true}, nil)
	check(e)
	defer upstream.Close()
	scope, e := configureStreamingConnector(transport, upstream.InitializeResult(), t.TempDir())
	check(e)
	defer scope.Close()
	host := &hostConnector{descriptor: &sharedMCPDescriptor{Endpoint: api.URL}, system: "fixture", profile: "agent:fixture", record: connectorRecord{Credential: "test-local", Principal: "local", Connected: true}, vault: localowner.Vault{Root: t.TempDir()}}
	proxy := newConnectorServer("unit2", upstream.InitializeResult())
	host.addMaterialTool(proxy, func(context.Context, []string) ([]contract.InformationCheck, error) { return nil, nil })
	var tools []*mcp.Tool
	for tool, e := range upstream.Tools(ctx, nil) {
		check(e)
		tools = append(tools, tool)
		copy := connectorTool(tool)
		proxy.AddTool(&copy, func(c context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return host.call(c, r, upstream)
		})
	}
	attach := func() func() {
		return host.attachOrganization(ctx, upstream.InitializeResult(), tools, func(c context.Context, name string, args any) (*mcp.CallToolResult, error) {
			if vectorFailure && name == "ownward_semantic_submit" {
				vectors.fail.Store(true)
			}
			r, err := organizationToolCall(c, scope, upstream, name, args)
			if vectorFailure && name == "ownward_semantic_submit" && err == nil && !r.IsError {
				var out mcpserver.SemanticSubmitOutput
				if err = decodeTool(r, &out); err != nil {
					return nil, err
				}
				submitStates <- out.Organization.Status
			}
			return r, err
		})
	}
	stop := attach()
	host.addOrganizationDemand(proxy)
	defer func() { stop() }()
	sr, cw := io.Pipe()
	cr, sw := io.Pipe()
	ioctx, iocancel := context.WithCancel(ctx)
	defer iocancel()
	done := make(chan error, 1)
	go func() { done <- runConnectorIO(ioctx, proxy, scope, sr, sw) }()
	defer func() {
		iocancel()
		sr.Close()
		sw.Close()
		cr.Close()
		cw.Close()
		<-done
	}()
	user, e := mcp.NewClient(&mcp.Implementation{Name: "fixture-host"}, nil).Connect(ctx, &mcp.IOTransport{Reader: cr, Writer: cw}, nil)
	check(e)
	defer user.Close()
	call := func(name string, args any, out any) {
		t.Helper()
		r, e := user.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		check(e)
		if r.IsError {
			b, _ := json.Marshal(r)
			t.Fatalf("%s: %s", name, b)
		}
		if out != nil {
			check(decodeTool(r, out))
		}
	}
	event := func(kind string) {
		var out map[string]any
		call("ownward_host_event", map[string]any{"event": kind, "session_id": "session"}, &out)
		if (kind == "Stop" || kind == "Interrupt" || kind == "SessionEnd") && len(out) != 0 {
			t.Fatalf("terminal hook must return the native empty response, got %v", out)
		}
	}
	create := func(content string) string {
		var out mcpserver.CreateOutput
		call("ownward_create", map[string]any{"content": content}, &out)
		if out.Result.Organization.RequiredAction != "ownward_semantic_jobs" {
			t.Fatal("not deferred", out)
		}
		return out.Result.Information.ID
	}
	wait := func(label string, f func() bool) {
		t.Helper()
		until := time.Now().Add(10 * time.Second)
		for time.Now().Before(until) {
			if f() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal(label, host.organization.state())
	}
	ready := func(id string) bool {
		var out mcpserver.StatusOutput
		call("ownward_status", map[string]any{"id": id}, &out)
		return out.Organization.Status == "ready"
	}
	wait("executor readiness", func() bool { return host.organization != nil && host.organization.ready.Load() })
	if demand {
		event("UserPromptSubmit")
		if len(scenario) > 0 && scenario[0] == "stale" {
			reservation, err := acquireServiceStartupLock(filepath.Join(host.organization.root, "machine.lock"), 3*time.Second)
			check(err)
			defer reservation.release()
			id := create("Queued dependency of a completed task")
			call("ownward_organize", map[string]any{"id": id}, nil)
			event("Stop")
			event("UserPromptSubmit")
			reservation.release()
			select {
			case got := <-started:
				t.Fatalf("obsolete dependency dispatched during unrelated foreground: %s", got)
			case <-time.After(1500 * time.Millisecond):
			}
			event("Stop")
			wait("ordinary retained background work did not resume", func() bool { return ready(id) })
			return
		}
		if len(scenario) > 0 && scenario[0] == "exhausted" {
			id := create("FAIL bounded recovery")
			call("ownward_organize", map[string]any{"id": id}, nil)
			var status mcpserver.StatusOutput
			wait("exhaustion not visible through normal status", func() bool {
				call("ownward_status", map[string]any{"id": id}, &status)
				return status.Organization.RequiredAction == "restore_organization_execution"
			})
			if status.Organization.Error == "" {
				t.Fatal("missing exhaustion reason")
			}
			call("ownward_organize", map[string]any{"id": id}, &status)
			if status.Organization.RequiredAction != "restore_organization_execution" {
				t.Fatal("exhaustion became requested")
			}
			var raw mcpserver.ReadOutput
			call("ownward_read", map[string]any{"id": id}, &raw)
			oldIdentity := status.Organization.ExecutionIdentity
			var update mcpserver.UpdateOutput
			call("ownward_update", map[string]any{"id": id, "expected_revision": raw.Information.Revision, "content": "Corrected source with valid organization"}, &update)
			call("ownward_status", map[string]any{"id": id}, &status)
			if status.Organization.RequiredAction == "restore_organization_execution" || status.Organization.ExecutionIdentity == oldIdentity {
				t.Fatal("old exhaustion contaminated new work")
			}
			call("ownward_organize", map[string]any{"id": id}, nil)
			wait("changed work failed to recover", func() bool { return ready(id) })
			other := create("Independent normal task")
			call("ownward_organize", map[string]any{"id": other}, nil)
			wait("exhausted work blocked unrelated source", func() bool { return ready(other) })
			return
		}
		content := "当前任务需要的资料，只组织一次。"
		if vectorFailure {
			content = strings.Repeat(content, 100)
		}
		id := create(content)
		if modelCalls.Load() != 0 {
			t.Fatal("unrelated work started during foreground")
		}
		call("ownward_organize", map[string]any{"id": id}, nil)
		call("ownward_organize", map[string]any{"id": id}, nil)
		wait("foreground dependency was never organized", func() bool { return ready(id) })
		call("ownward_organize", map[string]any{"id": id}, nil)
		active, err := host.organization.journal.foreground(ctx)
		check(err)
		if !active {
			t.Fatal("foreground ended before dependency completed")
		}
		if vectorFailure && vectors.failures.Load() != 1 {
			t.Fatal("vector failure not reached")
		}
		stop()
		if modelCalls.Load() != 1 {
			t.Fatal("same dependency duplicated AI work", modelCalls.Load())
		}
		return
	}
	if vectorFailure {
		event("UserPromptSubmit")
		id := create(strings.Repeat("项目星河负责人是李明，周五完成审核。", 100))
		event("Stop")
		select {
		case state := <-submitStates:
			if state != "pending" {
				t.Fatal("vector failure not reached", state)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("submission not observed")
		}
		wait("accepted semantics did not resume vectors", func() bool { return ready(id) })
		stop()
		if vectors.failures.Load() != 1 || modelCalls.Load() != 1 {
			t.Fatal("accepted semantics repeated model work", vectors.failures.Load(), modelCalls.Load())
		}
		t.Log("pending semantic submission resumed to ready with one model request")
		return
	}
	event("UserPromptSubmit")
	bad := create("FAIL fixture deliberately rejects incomplete response")
	good := create("项目星河负责人是李明。")
	if modelCalls.Load() != 0 {
		t.Fatal("foreground did not prevent new background dispatch")
	}
	event("Stop")
	wait("failed work starved good work", func() bool { return ready(good) })
	if ready(bad) {
		t.Fatal("invalid fixture became ready")
	}
	event("UserPromptSubmit")
	block.Store(true)
	interrupted := create("恢复后保留原文与正确的组织。")
	event("Stop")
	wait("interrupted work did not start", func() bool {
		for {
			select {
			case id := <-started:
				if id == interrupted {
					return true
				}
			default:
				return false
			}
		}
	})
	event("UserPromptSubmit")
	before := time.Now()
	var raw mcpserver.ReadOutput
	call("ownward_read", map[string]any{"id": good}, &raw)
	if raw.Information.Content != "项目星河负责人是李明。" || time.Since(before) > time.Second {
		t.Fatal("foreground blocked by model request", time.Since(before))
	}
	stop()
	block.Store(false)
	close(gate)
	stop = attach()
	wait("restart did not finish retained work", func() bool { return ready(interrupted) })
	event("UserPromptSubmit")
	retained := create("撤销后不应送入模型的材料。")
	beforeCalls := modelCalls.Load()
	revoked.Store(true)
	check(store.PublishAccess(ctx, boundedstore.AccessHeader{System: "fixture", Revision: 2}, 1, nil))
	stop()
	stop = attach()
	time.Sleep(250 * time.Millisecond)
	if modelCalls.Load() != beforeCalls || host.organization.ready.Load() {
		t.Fatal("revoked connection restarted model")
	}
	t.Logf("formal stdio path: failure isolation, reads during model request, restart and revocation; sources=%s,%s,%s,%s model calls=%d", bad, good, interrupted, retained, modelCalls.Load())
}

type organizationOneVectorFailure struct {
	embedding.HashForTesting
	fail     atomic.Bool
	failures atomic.Int32
}

func (v *organizationOneVectorFailure) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if v.fail.Swap(false) {
		v.failures.Add(1)
		return nil, errors.New("controlled one-time vector failure")
	}
	return v.HashForTesting.EmbedDocuments(ctx, texts)
}

func organizationFixtureWork(v any) (semantics.Work, bool) {
	switch v := v.(type) {
	case string:
		var w []semantics.Work
		if json.Unmarshal([]byte(v), &w) == nil && len(w) == 1 && w[0].Schema == semantics.WorkSchema {
			return w[0], true
		}
	case map[string]any:
		for _, x := range v {
			if w, ok := organizationFixtureWork(x); ok {
				return w, true
			}
		}
	case []any:
		for _, x := range v {
			if w, ok := organizationFixtureWork(x); ok {
				return w, true
			}
		}
	}
	return semantics.Work{}, false
}
