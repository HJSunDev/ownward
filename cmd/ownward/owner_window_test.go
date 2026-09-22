package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/adapter/ownerwindow"
	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/ownerview"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ownerHTTPFixture struct {
	s                    *boundedstore.Store
	c                    *informationcontrol.Control
	p                    *informationcontrol.Product
	k                    *core.StreamingAssets
	w                    *ownerwindow.Server
	server               *httptest.Server
	owner, session, root string
	ctx                  context.Context
}

func ownerHTTP(t *testing.T) *ownerHTTPFixture {
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "library")
	scratch := filepath.Join(root, "scratch")
	if e := os.MkdirAll(scratch, 0700); e != nil {
		t.Fatal(e)
	}
	b, e := resourcebudget.New(20*resourcebudget.MiB, 4*resourcebudget.MiB)
	if e != nil {
		t.Fatal(e)
	}
	s, e := boundedstore.OpenDeployment(ctx, root, boundedstore.DeploymentOptions{Options: boundedstore.Options{Budget: b}, Initialize: func(ctx context.Context, s *boundedstore.Store, _ string) error {
		_, e := s.OpenControlAuthority(ctx, contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "test"})
		return e
	}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	})
	a, e := s.OpenControlAuthority(ctx, contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "test"})
	if e != nil {
		t.Fatal(e)
	}
	c := informationcontrol.New(a)
	owner, e := c.InitializeOwner("物主")
	if e != nil {
		t.Fatal(e)
	}
	k := &core.StreamingAssets{Store: s, Budget: b, Scratch: scratch, DiskBytes: 256 * resourcebudget.MiB}
	p := informationcontrol.NewProduct(k, c)
	t.Cleanup(p.Close)
	v, e := ownerview.New(s, c, p)
	if e != nil {
		t.Fatal(e)
	}
	w := ownerwindow.New(v, localOwnerArchives(k, c, root))
	m := mcpserver.NewStreamingStorage(k, "test", scratch, b, k.DiskBytes)
	m.AddManagementTools(p)
	m.AddDraftTools(v)
	control := controlHTTPServer{server: m, control: c, product: p, kernel: k, window: w}
	server := httptest.NewUnstartedServer(nil)
	origin := "http://" + server.Listener.Addr().String()
	server.Config.Handler = mountOwnerWindow(bearerTokenHandler(control.HTTPHandler(), "transport-test"), w.Mount(origin))
	server.Start()
	t.Cleanup(server.Close)
	f := &ownerHTTPFixture{s: s, c: c, p: p, k: k, w: w, server: server, owner: owner, root: root, ctx: informationcontrol.Authenticate(ctx, owner)}
	f.session = f.login(t)
	return f
}

func (f *ownerHTTPFixture) bootstrap(t *testing.T) string {
	t.Helper()
	h := hostConnector{descriptor: &sharedMCPDescriptor{Endpoint: f.server.URL, BearerToken: "transport-test"}}
	var out struct {
		Entry string `json:"entry"`
	}
	if e := h.controlCall(context.Background(), "owner-window", f.owner, struct{}{}, &out); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(out.Entry)
	if e != nil {
		t.Fatal(e)
	}
	return u.Fragment
}
func (f *ownerHTTPFixture) login(t *testing.T) string {
	t.Helper()
	var out struct {
		Session string `json:"session"`
	}
	f.call(t, "bootstrap", map[string]string{"token": f.bootstrap(t)}, &out)
	if out.Session == "" {
		t.Fatal("no session")
	}
	return out.Session
}
func (f *ownerHTTPFixture) request(t *testing.T, path string, input any, mutate func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	b, e := json.Marshal(input)
	if e != nil {
		t.Fatal(e)
	}
	r, e := http.NewRequest("POST", f.server.URL+ownerwindow.Prefix+"v1/"+path, bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", f.server.URL)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-Ownward-View", contract.OwnerViewSchema)
	r.Header.Set("Authorization", "Bearer "+f.session)
	if mutate != nil {
		mutate(r)
	}
	response, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	body, e := io.ReadAll(response.Body)
	if e != nil {
		t.Fatal(e)
	}
	return response, body
}
func (f *ownerHTTPFixture) call(t *testing.T, path string, input, output any) {
	t.Helper()
	response, b := f.request(t, path, input, nil)
	if response.StatusCode != 200 {
		t.Fatalf("%s status=%d body=%s", path, response.StatusCode, b)
	}
	if output != nil {
		if e := json.Unmarshal(b, output); e != nil {
			t.Fatal(e, string(b))
		}
	}
}
func (f *ownerHTTPFixture) query(t *testing.T, q contract.OwnerQuery) contract.OwnerPage {
	t.Helper()
	var out contract.OwnerPage
	f.call(t, "query", q, &out)
	return out
}

// Fresh read projections during forget may require another read while the
// authority finishes cleanup. Exercise that contract without retrying writes
// or renewing a stale page/object handle on the caller's behalf.
func (f *ownerHTTPFixture) freshQuery(t *testing.T, q contract.OwnerQuery) contract.OwnerPage {
	t.Helper()
	if q.After != "" {
		t.Fatal("freshQuery cannot resume an old page")
	}
	for attempt := 0; attempt < 4; attempt++ {
		response, body := f.request(t, "query", q, nil)
		if response.StatusCode == http.StatusConflict {
			t.Log("fresh projection requested refresh")
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("fresh query status=%d body=%s", response.StatusCode, body)
		}
		var page contract.OwnerPage
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	t.Fatal("fresh projection did not recover within four reads")
	return contract.OwnerPage{}
}
func (f *ownerHTTPFixture) act(t *testing.T, in contract.OwnerAction) contract.OwnerResult {
	t.Helper()
	var out contract.OwnerResult
	f.call(t, "action", in, &out)
	return out
}
func textPtr(v string) *string { return &v }

func (f *ownerHTTPFixture) agent(t *testing.T, name string) (contract.Principal, string, *mcp.ClientSession) {
	t.Helper()
	p, credential, e := f.c.Enroll(f.ctx, name)
	if e != nil {
		t.Fatal(e)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: name}, nil)
	session, e := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: f.server.URL, HTTPClient: &http.Client{Transport: bearerTransport{token: "transport-test", base: http.DefaultTransport, credential: func() string { return credential }}}, DisableStandaloneSSE: true}, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { session.Close() })
	return p, credential, session
}

func TestOwnerWindowDraftGrantThroughHTTPAndMCP(t *testing.T) {
	f := ownerHTTP(t)
	p, credential, agent := f.agent(t, "同名接入者")
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("first draft🙂")})
	before := f.query(t, contract.OwnerQuery{View: "drafts"})
	connections := f.query(t, contract.OwnerQuery{View: "connections"})
	var principal string
	for _, v := range connections.Connections {
		if !v.Owner {
			principal = v.Handle
		}
	}
	grant := f.act(t, contract.OwnerAction{Action: "grant_draft", Handle: d.Handle, Target: principal, Seconds: 3600})
	var read contract.AgentDraftResult
	if e := decodeTool(hostCall(t, agent, "ownward_draft_work", contract.AgentDraftRequest{Action: "read", Draft: grant.Handle, Grant: grant.Grant}, true), &read); e != nil {
		t.Fatal(e)
	}
	if read.Content.Text != "first draft🙂" {
		t.Fatal(read)
	}
	hostCall(t, agent, "ownward_search", map[string]any{"query": "first draft"}, false)
	var written contract.AgentDraftResult
	write := contract.AgentDraftRequest{Action: "append", Draft: grant.Handle, Grant: grant.Grant, Handle: read.Handle, Text: " + agent"}
	if e := decodeTool(hostCall(t, agent, "ownward_draft_work", write, true), &written); e != nil {
		t.Fatal(e)
	}
	hostCall(t, agent, "ownward_draft_work", write, false) // uncertain retry cannot append twice
	after := f.query(t, contract.OwnerQuery{View: "drafts", Cursor: before.Cursor})
	if !after.Changed || len(after.Drafts) != 1 {
		t.Fatal(after)
	}
	page := f.query(t, contract.OwnerQuery{View: "draft_content", Handle: after.Drafts[0].Handle})
	if page.Text.Text != "first draft🙂 + agent" {
		t.Fatal(page.Text)
	}
	response, _ := f.request(t, "action", contract.OwnerAction{Action: "replace_draft", Handle: d.Handle, Text: textPtr("stale replacement")}, nil)
	if response.StatusCode != 409 {
		t.Fatalf("stale edit=%d", response.StatusCode)
	}
	published := f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: after.Drafts[0].Handle, OperationID: "publish-granted"})
	again := f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: after.Drafts[0].Handle, OperationID: "publish-granted"})
	if again.State != "completed" {
		t.Fatal(again)
	}
	hostCall(t, agent, "ownward_draft_work", contract.AgentDraftRequest{Action: "read", Draft: grant.Handle, Grant: grant.Grant}, false)
	if self, e := f.c.Self(informationcontrol.Authenticate(context.Background(), credential)); e != nil || len(self.Permissions) != 0 || self.ID != p.ID {
		t.Fatal("draft grant expanded asset rights", self, e)
	}
	if got := f.query(t, contract.OwnerQuery{View: "content", Handle: published.Handle}).Text.Text; got != "first draft🙂 + agent" {
		t.Fatal(got)
	}
	// Reauthenticate after clearing all client state; five projections rebuild
	// from authority, including the published draft and durable event.
	f.session = ""
	f.session = f.login(t)
	for _, view := range []string{"drafts", "assets", "overview", "events", "connections", "pending", "health"} {
		f.query(t, contract.OwnerQuery{View: view})
	}
	assets := f.query(t, contract.OwnerQuery{View: "assets"})
	events := f.query(t, contract.OwnerQuery{View: "events"})
	if len(assets.Assets) != 1 || len(events.Activity) == 0 {
		t.Fatalf("projections not reconstructable: assets=%+v events=%+v", assets.Assets, events.Activity)
	}
}

func TestOwnerWindowAuthenticationAndBoundedTransport(t *testing.T) {
	f := ownerHTTP(t)
	for _, path := range []string{"query", "action", "backup", "restore", "logout"} {
		t.Run("unauth-"+path, func(t *testing.T) {
			r, _ := f.request(t, path, map[string]string{"view": "health"}, func(r *http.Request) { r.Header.Del("Authorization") })
			if r.StatusCode != 401 {
				t.Fatal(r.StatusCode)
			}
		})
	}
	for name, modify := range map[string]func(*http.Request){
		"foreign-origin":      func(r *http.Request) { r.Header.Set("Origin", "http://evil.invalid") },
		"other-loopback-port": func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:1") },
		"dns-rebinding":       func(r *http.Request) { r.Host = "attacker.invalid" },
		"missing-origin":      func(r *http.Request) { r.Header.Del("Origin") },
		"missing-view-header": func(r *http.Request) { r.Header.Del("X-Ownward-View") },
		"cross-site-fetch":    func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"form":                func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := f.request(t, "query", contract.OwnerQuery{View: "health"}, modify)
			if r.StatusCode < 400 {
				t.Fatal(r.StatusCode)
			}
		})
	}
	bootstrap := f.bootstrap(t)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := f.request(t, "bootstrap", map[string]string{"token": bootstrap}, nil)
			codes <- r.StatusCode
		}()
	}
	wg.Wait()
	close(codes)
	success := 0
	for code := range codes {
		if code == 200 {
			success++
		}
	}
	if success != 1 {
		t.Fatal("bootstrap reused", success)
	}
	f.query(t, contract.OwnerQuery{View: "changes"})
	r, _ := f.request(t, "query", contract.OwnerQuery{View: "changes"}, nil)
	if r.StatusCode != 429 {
		t.Fatal("unbounded polling", r.StatusCode)
	}
	r, _ = f.request(t, "query", contract.OwnerQuery{View: "assets", Limit: 101}, nil)
	if r.StatusCode != 400 {
		t.Fatal("unbounded page", r.StatusCode)
	}
	r, b := f.request(t, "query", contract.OwnerQuery{View: "health"}, nil)
	if r.Header.Get("Cache-Control") != "no-store" || bytes.Contains(b, []byte(f.owner)) {
		t.Fatal("cache or credential leak")
	}
	if _, e := f.c.RecoverOwner(); e != nil {
		t.Fatal(e)
	}
	r, _ = f.request(t, "query", contract.OwnerQuery{View: "health"}, nil)
	if r.StatusCode != 401 {
		t.Fatal("old owner session survived", r.StatusCode)
	}
}

func TestOwnerWindowTextPagingFindAndInvalidation(t *testing.T) {
	f := ownerHTTP(t)
	text := strings.Repeat("甲🙂核对 common ", 9000)
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: &text})
	var recovered strings.Builder
	offset := int64(0)
	for {
		p := f.query(t, contract.OwnerQuery{View: "draft_content", Handle: d.Handle, Offset: offset})
		recovered.WriteString(p.Text.Text)
		if len(p.Text.Text) > contract.OwnerTextBytes {
			t.Fatal("oversize page")
		}
		offset = p.Text.NextOffset
		if !p.Text.More {
			break
		}
	}
	if recovered.String() != text {
		t.Fatal("UTF-8 pagination changed text")
	}
	f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "long"})
	for i := 0; i < 18; i++ {
		d = f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("common 共同词")})
		f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "item-" + strings.Repeat("x", i+1)})
	}
	q := contract.OwnerQuery{View: "assets", Query: "common", Limit: 5}
	first := f.query(t, q)
	count := len(first.Assets)
	q.After = first.Next
	for q.After != "" {
		p := f.query(t, q)
		count += len(p.Assets)
		q.After = p.Next
	}
	if count != 19 {
		t.Fatalf("common word suppressed or page omitted: %d", count)
	}
	f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("new change")})
	q.After = first.Next
	r, _ := f.request(t, "query", q, nil)
	if r.StatusCode != 409 {
		t.Fatal("old cursor survived", r.StatusCode)
	}
}

func TestOwnerWindowApprovesSameHostRequestAndResumes(t *testing.T) {
	f := ownerHTTP(t)
	configuration := t.TempDir()
	t.Setenv("APPDATA", configuration)
	t.Setenv("XDG_CONFIG_HOME", configuration)
	vault, e := localowner.Default()
	if e != nil {
		t.Fatal(e)
	}
	if e = vault.Save(f.c.SystemID(), "owner", f.owner); e != nil {
		t.Fatal(e)
	}
	h := &hostConnector{descriptor: &sharedMCPDescriptor{Endpoint: f.server.URL, BearerToken: "transport-test"}, vault: vault, system: f.c.SystemID(), profile: "isolated-confirmation-test"}
	p, credential, e := f.c.Enroll(f.ctx, "awaiting-host")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.p.Manage(informationcontrol.Authenticate(context.Background(), credential), contract.ManagementRequest{ID: "same-request", Operation: "permissions", SubjectID: p.ID, Permissions: []contract.Permission{contract.ReadPermission}}); e != nil {
		t.Fatal(e)
	}
	opened := make(chan struct{})
	resumed := make(chan struct{}, 1)
	server := mcp.NewServer(&mcp.Implementation{Name: "host-test"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "resume"}, func(ctx context.Context, r *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]bool, error) {
		_, e := h.confirm(ctx, r, "same-request")
		if e == nil {
			resumed <- struct{}{}
		}
		return nil, map[string]bool{"resumed": e == nil}, e
	})
	ct, st := mcp.NewInMemoryTransports()
	ss, e := server.Connect(context.Background(), st, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "simulated-no-link-host"}, &mcp.ClientOptions{ElicitationHandler: func(ctx context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		close(opened)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	cs, e := client.Connect(context.Background(), ct, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer cs.Close()
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		r, e := cs.CallTool(ctx, &mcp.CallToolParams{Name: "resume", Arguments: struct{}{}})
		if e == nil && r.IsError {
			data, _ := json.Marshal(r)
			e = fmt.Errorf("host result: %s", data)
		}
		done <- e
	}()
	select {
	case <-opened:
	case e := <-done:
		t.Fatalf("host ended before presenting form: %v", e)
	case <-time.After(4 * time.Second):
		t.Fatal("host form not presented")
	}
	page := f.query(t, contract.OwnerQuery{View: "pending"})
	if len(page.Decisions) != 1 {
		t.Fatal(page)
	}
	f.act(t, contract.OwnerAction{Action: "decide", Handle: page.Decisions[0].Handle, Accept: true})
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	select {
	case <-resumed:
	default:
		t.Fatal("original task not resumed")
	}
	if e := f.c.SetPermissions(f.ctx, p.ID, nil); e != nil {
		t.Fatal(e)
	}
	// A late host decision cannot resurrect the already completed grant.
	late, e := f.p.Decide(f.ctx, "same-request", true)
	if e != nil || late.Status != "completed" {
		t.Fatal(late, e)
	}
	self, e := f.c.Self(informationcontrol.Authenticate(context.Background(), credential))
	if e != nil || len(self.Permissions) != 0 {
		t.Fatal("late decision reapplied rights", self, e)
	}
}

func TestOwnerWindowBackupRestoreUsesNoKernelRebuild(t *testing.T) {
	f := ownerHTTP(t)
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("survive archive")})
	response, archive := f.request(t, "backup", struct{}{}, nil)
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode, string(archive))
	}
	r, e := http.NewRequest("POST", f.server.URL+ownerwindow.Prefix+"v1/restore", bytes.NewReader(archive))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Content-Type", "application/zip")
	r.Header.Set("Origin", f.server.URL)
	r.Header.Set("X-Ownward-View", contract.OwnerViewSchema)
	r.Header.Set("Authorization", "Bearer "+f.session)
	result, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer result.Body.Close()
	var restored struct {
		State string `json:"state"`
		Dir   string `json:"data_dir"`
	}
	if e = json.NewDecoder(result.Body).Decode(&restored); e != nil {
		t.Fatal(e)
	}
	if result.StatusCode != 200 || restored.State != "verification_required" {
		t.Fatal(result.StatusCode, restored)
	}
	if restored.Dir == f.root {
		t.Fatal("overwrote live authority")
	}
	if _, e = os.Stat(filepath.Join(restored.Dir, "storage.json")); e != nil {
		t.Fatal(e)
	}
	if f.query(t, contract.OwnerQuery{View: "draft_content", Handle: d.Handle}).Text.Text != "survive archive" {
		t.Fatal("live draft disturbed")
	}
	b, _ := resourcebudget.New(16*resourcebudget.MiB, 4*resourcebudget.MiB)
	store, e := boundedstore.OpenDeployment(context.Background(), restored.Dir, boundedstore.DeploymentOptions{Options: boundedstore.Options{Budget: b}})
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	a, e := store.OpenControlAuthority(context.Background(), contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "test"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.ListDrafts(f.ctx, "", 10); e == nil {
		t.Fatal("restored archive accepted old owner")
	}
	recovered := informationcontrol.New(a)
	credential, e := recovered.RecoverOwner()
	if e != nil {
		t.Fatal(e)
	}
	page, e := store.ListDrafts(informationcontrol.Authenticate(context.Background(), credential), "", 10)
	if e != nil || len(page.Items) != 1 {
		t.Fatal("restored draft unavailable", page, e)
	}
}

func TestOwnerWindowEnrollmentAndMigrationDecisionsStayAuthoritative(t *testing.T) {
	f := ownerHTTP(t)
	manager, credential, e := f.c.Enroll(f.ctx, "授权管理者")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.c.SetPermissions(f.ctx, manager.ID, []contract.Permission{contract.ManagePermission}); e != nil {
		t.Fatal(e)
	}
	managerCtx := informationcontrol.Authenticate(context.Background(), credential)
	if _, e = f.c.Invite(managerCtx, "new-device"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.c.Join("new-device", strings.Repeat("test-proof", 4), "新连接", []contract.Permission{contract.ReadPermission}); e != nil {
		t.Fatal(e)
	}
	page := f.query(t, contract.OwnerQuery{View: "pending"})
	if len(page.Decisions) != 1 || page.Decisions[0].Kind != "enrollment" {
		t.Fatal(page.Decisions)
	}
	_, marker, e := f.c.EnrollmentPreview(f.ctx, "new-device")
	if e != nil || marker == "" || page.Decisions[0].Verification != marker {
		t.Fatal("owner cannot verify the target device", page.Decisions, e)
	}
	f.act(t, contract.OwnerAction{Action: "decide", Handle: page.Decisions[0].Handle, Accept: true})
	owner, e := f.c.Owner(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	claim, token, e := f.c.ClaimEnrollment("new-device", strings.Repeat("test-proof", 4))
	if e != nil || token == "" || claim.Approver != owner.ID || claim.Approver == manager.ID {
		t.Fatal("owner approval misattributed", claim, e)
	}
	target := contract.Location{ServiceID: "destination", SystemID: f.c.SystemID(), Endpoint: "https://isolated.test", Certificate: "fixture", Composition: "test"}
	handoff, e := f.c.PrepareHandoff(managerCtx, "move-window", target)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.c.FreezeHandoff(managerCtx, handoff.ID, true); e == nil {
		t.Fatal("unapproved migration started")
	}
	page = f.query(t, contract.OwnerQuery{View: "pending"})
	if len(page.Decisions) != 1 || page.Decisions[0].Kind != "handoff" {
		t.Fatal(page.Decisions)
	}
	f.act(t, contract.OwnerAction{Action: "decide", Handle: page.Decisions[0].Handle, Accept: true})
	late, e := f.c.DecideHandoff(managerCtx, handoff.ID, handoff.Revision, false)
	if e != nil || late.ApprovalStatus != "approved" || late.Approver != owner.ID {
		t.Fatal("late confirmation overrode owner", late, e)
	}
	if _, e = f.c.FreezeHandoff(managerCtx, handoff.ID, true); e != nil {
		t.Fatal(e)
	}
}

func TestOwnerWindowReapprovesMigrationAfterOwnerRecovery(t *testing.T) {
	f := ownerHTTP(t)
	target := contract.Location{ServiceID: "destination", SystemID: f.c.SystemID(), Endpoint: "https://isolated.test", Certificate: "fixture", Composition: "test"}
	h, e := f.c.PrepareHandoff(f.ctx, "recover-move", target)
	if e != nil {
		t.Fatal(e)
	}
	old := f.query(t, contract.OwnerQuery{View: "pending"}).Decisions[0].Handle
	f.act(t, contract.OwnerAction{Action: "decide", Handle: old, Accept: true})
	if f.owner, e = f.c.RecoverOwner(); e != nil {
		t.Fatal(e)
	}
	f.ctx = informationcontrol.Authenticate(context.Background(), f.owner)
	f.session = f.login(t)
	if _, e = f.c.FreezeHandoff(f.ctx, h.ID, true); e == nil {
		t.Fatal("accepted stale approval")
	}
	page := f.query(t, contract.OwnerQuery{View: "pending"})
	if len(page.Decisions) != 1 {
		t.Fatal("lost invalidated approval", page)
	}
	response, _ := f.request(t, "action", contract.OwnerAction{Action: "decide", Handle: old, Accept: true}, nil)
	if response.StatusCode != 409 {
		t.Fatal("old decision reused", response.StatusCode)
	}
	f.act(t, contract.OwnerAction{Action: "decide", Handle: page.Decisions[0].Handle, Accept: true})
	if _, e = f.c.FreezeHandoff(f.ctx, h.ID, true); e != nil {
		t.Fatal(e)
	}
}

func TestOwnerWindowDeclineMigrationWithoutHostReleasesAuthority(t *testing.T) {
	f := ownerHTTP(t)
	target := contract.Location{ServiceID: "destination", SystemID: f.c.SystemID(), Endpoint: "https://isolated.test", Certificate: "fixture", Composition: "test"}
	h, e := f.c.PrepareHandoff(f.ctx, "declined-move", target)
	if e != nil {
		t.Fatal(e)
	}
	before := f.query(t, contract.OwnerQuery{View: "pending"})
	cleaned := make(chan struct{}, 1)
	ready := make(chan struct{}, 1)
	f.p.SetRelatedCleanup(func() error {
		select {
		case ready <- struct{}{}:
		default:
		}
		state := f.c.State()
		for _, old := range state.Access.Cancelled {
			if old.ID == h.ID && !old.Cleaned {
				if e := f.c.MarkHandoffClean(h.ID); e != nil {
					return e
				}
				select {
				case cleaned <- struct{}{}:
				default:
				}
			}
		}
		return nil
	})
	<-ready
	decision := f.act(t, contract.OwnerAction{Action: "decide", Handle: before.Decisions[0].Handle, Accept: false})
	if decision.State != "declined" {
		t.Fatal(decision)
	}
	select {
	case <-cleaned:
	case <-time.After(2 * time.Second):
		t.Fatal("decline did not wake cancellation cleanup")
	}
	if page := f.query(t, contract.OwnerQuery{View: "pending"}); len(page.Decisions) != 0 {
		t.Fatal(page)
	}
	// A fresh control instance reads the terminal receipt after cleanup; neither
	// a late approval nor a cancel retry can touch the next migration.
	a, e := f.s.OpenControlAuthority(f.ctx, f.c.State())
	if e != nil {
		t.Fatal(e)
	}
	reloaded := informationcontrol.New(a)
	old, e := reloaded.HandoffStatus(f.ctx, h.ID)
	if e != nil || old.Phase != "cancelled" || old.ApprovalStatus != "declined" {
		t.Fatal(old, e)
	}
	if _, e = reloaded.PrepareHandoff(f.ctx, "next-move", target); e != nil {
		t.Fatal(e)
	}
	late, e := reloaded.DecideHandoff(f.ctx, h.ID, h.Revision, true)
	if e != nil || late.ApprovalStatus != "declined" {
		t.Fatal(late, e)
	}
	if e = reloaded.CancelHandoff(f.ctx, h.ID); e != nil {
		t.Fatal(e)
	}
	if active := reloaded.State().Access.Handoff; active == nil || active.ID != "next-move" {
		t.Fatal(active)
	}
	if _, e = reloaded.FreezeHandoff(f.ctx, h.ID, true); e == nil {
		t.Fatal("declined handoff restarted")
	}
}

func TestOwnerWindowEnrollmentReapprovalAfterRecovery(t *testing.T) {
	for _, stage := range []string{"unissued", "issued", "revoked", "acknowledged"} {
		t.Run(stage, func(t *testing.T) {
			f := ownerHTTP(t)
			proof := strings.Repeat("synthetic-proof", 4)
			if _, e := f.c.Invite(f.ctx, "resume-join"); e != nil {
				t.Fatal(e)
			}
			if _, e := f.c.Join("resume-join", proof, "new connection", []contract.Permission{contract.ReadPermission}); e != nil {
				t.Fatal(e)
			}
			before, marker, e := f.c.EnrollmentPreview(f.ctx, "resume-join")
			if e != nil {
				t.Fatal(e)
			}
			page := f.query(t, contract.OwnerQuery{View: "pending"})
			oldHandle := page.Decisions[0].Handle
			f.act(t, contract.OwnerAction{Action: "decide", Handle: oldHandle, Accept: true})
			var issued contract.Enrollment
			var credential string
			if stage != "unissued" {
				issued, credential, e = f.c.ClaimEnrollment("resume-join", proof)
				if e != nil || credential == "" {
					t.Fatal(issued, e)
				}
			}
			if stage == "acknowledged" {
				if e = f.c.AcknowledgeEnrollment(informationcontrol.Authenticate(context.Background(), credential), "resume-join", proof); e != nil {
					t.Fatal(e)
				}
			}
			if stage == "revoked" {
				if e = f.c.SetPermissions(f.ctx, issued.Principal, nil); e != nil {
					t.Fatal(e)
				}
			}
			f.owner, e = f.c.RecoverOwner()
			if e != nil {
				t.Fatal(e)
			}
			f.ctx = informationcontrol.Authenticate(context.Background(), f.owner)
			f.session = f.login(t)
			claim, token, e := f.c.ClaimEnrollment("resume-join", proof)
			if e != nil || token != "" {
				t.Fatal("old approval issued credential", claim, e)
			}
			page = f.query(t, contract.OwnerQuery{View: "pending"})
			if stage == "acknowledged" {
				if len(page.Decisions) != 0 {
					t.Fatal("completed enrollment reopened", page)
				}
				return
			}
			if claim.Status != "pending" || len(page.Decisions) != 1 {
				t.Fatal("lost reapproval", claim, page)
			}
			preview, currentMarker, e := f.c.EnrollmentPreview(f.ctx, "resume-join")
			if e != nil || currentMarker != marker || preview.Decision == before.Decision {
				t.Fatal(preview, e)
			}
			if _, e = f.c.DecideEnrollmentVersion(f.ctx, "resume-join", marker, before.Decision, true); e == nil {
				t.Fatal("old form revived approval")
			}
			if _, e = f.c.DecideEnrollment(f.ctx, "resume-join", marker, true); e == nil {
				t.Fatal("unversioned form revived approval")
			}
			r, _ := f.request(t, "action", contract.OwnerAction{Action: "decide", Handle: oldHandle, Accept: true}, nil)
			if r.StatusCode != 409 {
				t.Fatal("old window handle reused", r.StatusCode)
			}
			f.act(t, contract.OwnerAction{Action: "decide", Handle: page.Decisions[0].Handle, Accept: true})
			claim, token, e = f.c.ClaimEnrollment("resume-join", proof)
			if stage == "revoked" {
				if e == nil || token != "" {
					t.Fatal("reapproval restored revoked recipient", claim, e)
				}
				return
			}
			if e != nil || token == "" || (issued.Principal != "" && issued.Principal != claim.Principal) {
				t.Fatal("same enrollment did not resume", claim, e)
			}
			if credential != "" {
				if _, e := f.c.Self(informationcontrol.Authenticate(context.Background(), credential)); e == nil {
					t.Fatal("old recipient credential survived reissue")
				}
			}
		})
	}
}

func TestOwnerWindowEnrollmentUsesActualApproverAndRecipientVersions(t *testing.T) {
	f := ownerHTTP(t)
	manager, token, e := f.c.Enroll(f.ctx, "delegated manager")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.c.SetPermissions(f.ctx, manager.ID, []contract.Permission{contract.ManagePermission}); e != nil {
		t.Fatal(e)
	}
	managerCtx := informationcontrol.Authenticate(context.Background(), token)
	proof := strings.Repeat("test-proof", 4)
	if _, e = f.c.Invite(managerCtx, "delegated-join"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.c.Join("delegated-join", proof, "new reader", []contract.Permission{contract.ReadPermission}); e != nil {
		t.Fatal(e)
	}
	v, marker, e := f.c.EnrollmentPreview(managerCtx, "delegated-join")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.c.DecideEnrollmentVersion(managerCtx, v.ID, marker, v.Decision, true); e != nil {
		t.Fatal(e)
	}
	if p := f.query(t, contract.OwnerQuery{View: "pending"}); len(p.Decisions) != 0 {
		t.Fatal("valid delegated approval reopened", p)
	}
	issued, _, e := f.c.ClaimEnrollment(v.ID, proof)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.c.SetPermissions(f.ctx, manager.ID, nil); e != nil {
		t.Fatal(e)
	}
	p := f.query(t, contract.OwnerQuery{View: "pending"})
	if len(p.Decisions) != 1 {
		t.Fatal("revoked approver not visible", p)
	}
	v, marker, e = f.c.EnrollmentPreview(f.ctx, v.ID)
	if e != nil {
		t.Fatal(e)
	}
	// The public marker remains unchanged, but the old decision cannot grant
	// against a recipient whose permissions changed while the card was open.
	if e = f.c.SetPermissions(f.ctx, issued.Principal, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = f.c.DecideEnrollmentVersion(f.ctx, v.ID, marker, v.Decision, true); e == nil {
		t.Fatal("stale recipient decision accepted")
	}
	r, _ := f.request(t, "action", contract.OwnerAction{Action: "decide", Handle: p.Decisions[0].Handle, Accept: true}, nil)
	if r.StatusCode != 409 {
		t.Fatal("stale recipient card accepted", r.StatusCode)
	}
}

func TestOwnerWindowManagementByteBoundDoesNotHideDecisions(t *testing.T) {
	f := ownerHTTP(t)
	_, credential, e := f.c.Enroll(f.ctx, "requester")
	if e != nil {
		t.Fatal(e)
	}
	agent := informationcontrol.Authenticate(context.Background(), credential)
	for n := 0; n < 3; n++ {
		request := contract.ManagementRequest{ID: fmt.Sprintf("large-%d", n), Operation: "forget"}
		for i := 0; i < contract.MaxForgetTargets; i++ {
			request.Targets = append(request.Targets, contract.AssetVersion{ID: fmt.Sprintf("%s-%04d", strings.Repeat("x", 2800), i), Revision: 1})
		}
		if _, e = f.c.Propose(agent, request); e != nil {
			t.Fatal(e)
		}
	}
	q := contract.OwnerQuery{View: "pending", Limit: 30}
	count := 0
	for i := 0; i < 5; i++ {
		p := f.query(t, q)
		count += len(p.Decisions)
		if p.Next == "" {
			break
		}
		q.After = p.Next
	}
	if count != 3 {
		t.Fatal("byte-bound pagination lost decisions", count)
	}
}

func TestOwnerWindowEventsContinueAcrossNewWrites(t *testing.T) {
	f := ownerHTTP(t)
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("first")})
	f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "event-first"})
	before := f.query(t, contract.OwnerQuery{View: "events"})
	if len(before.Activity) != 2 || before.Next == "" {
		t.Fatal(before)
	}
	d = f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("second")})
	f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "event-second"})
	after := f.query(t, contract.OwnerQuery{View: "events", After: before.Next})
	if len(after.Activity) != 2 {
		t.Fatal("old events reread or new events skipped", after)
	}
	end := f.query(t, contract.OwnerQuery{View: "events", After: after.Next})
	if len(end.Activity) != 0 || end.Next == "" {
		t.Fatal(end)
	}
}

func TestOwnerWindowExpiryInvalidatesIdleCursor(t *testing.T) {
	f := ownerHTTP(t)
	if _, e := f.c.Invite(f.ctx, "expiring"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.c.Join("expiring", strings.Repeat("p", 32), "device", []contract.Permission{contract.ReadPermission}); e != nil {
		t.Fatal(e)
	}
	a, e := f.s.OpenControlAuthority(context.Background(), contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "test"})
	if e != nil {
		t.Fatal(e)
	}
	state := a.ReadControl()
	expires := time.Now().Add(1200 * time.Millisecond)
	state.Access.Enrollments[0].Expires = expires
	previous := state.Revision
	state.Revision++
	if _, e = a.CompareAndSwapControl(previous, state); e != nil {
		t.Fatal(e)
	}
	before := f.query(t, contract.OwnerQuery{View: "pending"})
	if len(before.Decisions) != 1 {
		t.Fatal(before)
	}
	unchanged := f.query(t, contract.OwnerQuery{View: "health", Cursor: before.Cursor})
	if unchanged.Changed {
		t.Fatal("unchanged cursor invalidated")
	}
	time.Sleep(time.Until(expires) + 30*time.Millisecond)
	change := f.query(t, contract.OwnerQuery{View: "changes", Cursor: before.Cursor})
	if !change.Changed {
		t.Fatal("expiration invisible without a write")
	}
	if p := f.query(t, contract.OwnerQuery{View: "pending"}); len(p.Decisions) != 0 {
		t.Fatal(p)
	}
}

func TestOwnerWindowEntryRebuiltFromAuthority(t *testing.T) {
	f := ownerHTTP(t)
	server := controlHTTPServer{control: f.c, window: f.w, dataDir: f.root}
	if e := server.PublishOwnerEntry(f.server.URL); e != nil {
		t.Fatal(e)
	}
	entry, e := discoverOwnerEntry(f.root, f.c.SystemID(), f.server.URL, nil)
	if e != nil || entry.Entry != f.server.URL+ownerwindow.Prefix {
		t.Fatal(entry, e)
	}
	if e = os.WriteFile(filepath.Join(f.root, "owner-window.json"), []byte(`{"entry":"http://untrusted.test"}`), 0600); e != nil {
		t.Fatal(e)
	}
	entry, e = discoverOwnerEntry(f.root, f.c.SystemID(), f.server.URL, nil)
	if e != nil || entry.Entry != f.server.URL+ownerwindow.Prefix {
		t.Fatal("untrusted hint was followed", entry, e)
	}
	target := contract.Location{SystemID: f.c.SystemID(), ServiceID: "next", Endpoint: "https://new-location.test", Certificate: "fixture", Composition: "test"}
	h := &contract.Handoff{Phase: "retired", Target: target}
	entry, e = discoverOwnerEntry(f.root, f.c.SystemID(), "", h)
	if e != nil || entry.Entry != "" || entry.Target == nil || *entry.Target != target {
		t.Fatal("old location not retired", entry, e)
	}
	// Destination / restored activation regenerates a local location and never
	// reuses an archived device session or the source machine's loopback URL.
	entry, e = discoverOwnerEntry(f.root, f.c.SystemID(), "http://127.0.0.1:12345", nil)
	if e != nil || entry.Target != nil || entry.Entry != "http://127.0.0.1:12345"+ownerwindow.Prefix {
		t.Fatal(entry, e)
	}
}

func TestOwnerWindowDamagedHintDoesNotBlockMachineService(t *testing.T) {
	f := ownerHTTP(t)
	if e := os.Mkdir(filepath.Join(f.root, "owner-window.json"), 0700); e != nil {
		t.Fatal(e)
	}
	server := controlHTTPServer{server: mcpserver.NewStreamingStorage(f.k, "test", f.k.Scratch, f.k.Budget, f.k.DiskBytes), control: f.c, window: f.w, dataDir: f.root}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() {
		e := runHTTPMCP(ctx, server, "127.0.0.1:0", "transport-test", writer)
		writer.CloseWithError(e)
		done <- e
	}()
	var address struct {
		Endpoint string `json:"endpoint"`
	}
	if e := json.NewDecoder(reader).Decode(&address); e != nil {
		t.Fatal("window hint blocked service", e)
	}
	h := hostConnector{descriptor: &sharedMCPDescriptor{Endpoint: address.Endpoint, BearerToken: "transport-test"}}
	var identity struct {
		System string `json:"system_id"`
	}
	if e := h.controlCall(ctx, "identity", "", nil, &identity); e != nil || identity.System != f.c.SystemID() {
		t.Fatal(identity, e)
	}
	cancel()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestOwnerWindowGrantAndConnectionIdentitySurviveReopen(t *testing.T) {
	f := ownerHTTP(t)
	first, _, agent := f.agent(t, "同名连接")
	if _, _, e := f.c.Enroll(f.ctx, "同名连接"); e != nil {
		t.Fatal(e)
	}
	connections := f.query(t, contract.OwnerQuery{View: "connections"}).Connections
	var matching []contract.OwnerConnection
	for _, p := range connections {
		if p.Name == "同名连接" {
			matching = append(matching, p)
		}
	}
	if len(matching) != 2 || matching[0].Distinction == "" || matching[0].Distinction == matching[1].Distinction {
		t.Fatal("indistinguishable connections", matching)
	}
	registered, e := f.s.OwnerPrincipal(f.ctx, first.ID)
	if e != nil {
		t.Fatal(e)
	}
	if matching[0].Distinction != fmt.Sprintf("第 %d 个登记的连接", registered.Order) {
		matching[0], matching[1] = matching[1], matching[0]
	}
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("private work")})
	g := f.act(t, contract.OwnerAction{Action: "grant_draft", Handle: d.Handle, Target: matching[0].Handle, Seconds: 3600})
	hostCall(t, agent, "ownward_draft_work", contract.AgentDraftRequest{Action: "read", Draft: g.Handle, Grant: g.Grant}, true)
	f.call(t, "logout", struct{}{}, nil)
	f.session = "" // discard all client state except the fixture's owner channel
	f.session = f.login(t)
	rebuilt := f.query(t, contract.OwnerQuery{View: "connections"})
	labels := map[string]bool{}
	for _, p := range rebuilt.Connections {
		if p.Name == "同名连接" {
			labels[p.Distinction] = true
		}
	}
	if !labels[matching[0].Distinction] || !labels[matching[1].Distinction] {
		t.Fatal("connection labels changed", labels)
	}
	grants := f.query(t, contract.OwnerQuery{View: "draft_grants", Limit: 1})
	if len(grants.Grants) != 1 || grants.Grants[0].Connection.Distinction != matching[0].Distinction || grants.Grants[0].ExpiresAt.IsZero() {
		t.Fatal("grant lost after reconnect", grants)
	}
	if f.query(t, contract.OwnerQuery{View: "draft_content", Handle: grants.Grants[0].Draft}).Text.Text != "private work" {
		t.Fatal("grant draft mismatch")
	}
	f.act(t, contract.OwnerAction{Action: "revoke_grant", Handle: grants.Grants[0].Handle})
	if p := f.query(t, contract.OwnerQuery{View: "draft_grants"}); len(p.Grants) != 0 {
		t.Fatal("revoked grant still visible", p)
	}
	hostCall(t, agent, "ownward_draft_work", contract.AgentDraftRequest{Action: "read", Draft: g.Handle, Grant: g.Grant}, false)
	// Target exactly the stable connection label after a new device session.
	for _, p := range rebuilt.Connections {
		if p.Distinction == matching[1].Distinction {
			f.act(t, contract.OwnerAction{Action: "permissions", Handle: p.Handle, Permissions: []contract.Permission{contract.ReadPermission}, OperationID: "distinguished-connection"})
		}
	}
	for _, p := range f.query(t, contract.OwnerQuery{View: "connections"}).Connections {
		if p.Distinction == matching[0].Distinction && len(p.Permissions) != 0 {
			t.Fatal("changed wrong same-name connection")
		}
		if p.Distinction == matching[1].Distinction && len(p.Permissions) != 1 {
			t.Fatal("did not change selected connection")
		}
	}
}

func TestOwnerWindowGrantExpiryAndManagementActivity(t *testing.T) {
	f := ownerHTTP(t)
	p, _, _ := f.agent(t, "activity-device")
	var connection contract.OwnerConnection
	for _, v := range f.query(t, contract.OwnerQuery{View: "connections"}).Connections {
		if v.Name == p.Name {
			connection = v
		}
	}
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("expiring grant")})
	f.act(t, contract.OwnerAction{Action: "grant_draft", Handle: d.Handle, Target: connection.Handle, Seconds: 1})
	before := f.query(t, contract.OwnerQuery{View: "draft_grants"})
	if len(before.Grants) != 1 {
		t.Fatal(before)
	}
	time.Sleep(time.Until(before.Grants[0].ExpiresAt) + 30*time.Millisecond)
	change := f.query(t, contract.OwnerQuery{View: "changes", Cursor: before.Cursor})
	if !change.Changed {
		t.Fatal("grant expiry invisible")
	}
	if p := f.query(t, contract.OwnerQuery{View: "draft_grants"}); len(p.Grants) != 0 {
		t.Fatal(p)
	}
	f.act(t, contract.OwnerAction{Action: "permissions", Handle: connection.Handle, Permissions: []contract.Permission{contract.ReadPermission}, OperationID: "activity-permission"})
	a := f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "activity-publish"})
	f.act(t, contract.OwnerAction{Action: "forget", Handle: a.Handle, OperationID: "activity-forget"})
	events := f.freshQuery(t, contract.OwnerQuery{View: "events"})
	permissions, forget := false, false
	for _, v := range events.Activity {
		switch v.Kind {
		case "permissions":
			permissions = true
			if v.Subject != p.Name || v.Distinction != connection.Distinction {
				t.Fatal("authorization object lost", v)
			}
		case "forget":
			forget = true
			if v.TargetCount != 1 || v.UnavailableTargets != 1 || v.Asset != "" {
				t.Fatal("forget activity misrepresented", v)
			}
		case "management":
			t.Fatal("undifferentiated management activity", v)
		}
	}
	if !permissions || !forget {
		t.Fatal("missing business activity", events)
	}
}

func TestOwnerWindowGrantRevocationInvalidatesFrozenHandoff(t *testing.T) {
	f := ownerHTTP(t)
	f.agent(t, "moving-worker")
	var principal string
	for _, p := range f.query(t, contract.OwnerQuery{View: "connections"}).Connections {
		if p.Name == "moving-worker" {
			principal = p.Handle
		}
	}
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("work in transit")})
	f.act(t, contract.OwnerAction{Action: "grant_draft", Handle: d.Handle, Target: principal, Seconds: 3600})
	grant := f.query(t, contract.OwnerQuery{View: "draft_grants"}).Grants[0].Handle
	target := contract.Location{SystemID: f.c.SystemID(), ServiceID: "next", Endpoint: "https://next.test", Certificate: "fixture", Composition: "test"}
	h, e := f.c.PrepareHandoff(f.ctx, "grant-in-flight", target)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.c.DecideHandoff(f.ctx, h.ID, h.Revision, true); e != nil {
		t.Fatal(e)
	}
	h, e = f.c.FreezeHandoff(f.ctx, h.ID, true)
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := f.s.Snapshot(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	changed := f.c.Changed()
	cleaned := make(chan struct{}, 1)
	f.p.SetRelatedCleanup(func() error {
		state := f.c.State()
		if state.Access != nil {
			for _, cancelled := range state.Access.Cancelled {
				if cancelled.ID == h.ID && !cancelled.Cleaned {
					if e := f.c.MarkHandoffClean(h.ID); e != nil {
						return e
					}
					select {
					case cleaned <- struct{}{}:
					default:
					}
				}
			}
		}
		return nil
	})
	f.act(t, contract.OwnerAction{Action: "revoke_grant", Handle: grant})
	select {
	case <-changed:
	default:
		t.Fatal("migration observers were not notified")
	}
	if _, e = f.c.RetireHandoff(f.ctx, h.ID, snapshot.SHA256, h.Revision); e == nil {
		t.Fatal("revoked grant could revive through the old migration snapshot")
	}
	state := f.c.State()
	if state.Access.Handoff != nil {
		t.Fatal("revocation did not cancel frozen candidate", state.Access)
	}
	if p := f.query(t, contract.OwnerQuery{View: "draft_grants"}); len(p.Grants) != 0 {
		t.Fatal(p)
	}
	select {
	case <-cleaned:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled-copy cleanup was not resumed")
	}
	f.act(t, contract.OwnerAction{Action: "revoke_grant", Handle: grant})
}

func TestOwnerWindowRetiredSourceCannotReportGrantRevoked(t *testing.T) {
	f := ownerHTTP(t)
	p, _, _ := f.agent(t, "retired-worker")
	d, e := f.s.CreateDraft(f.ctx, contract.DraftInput{Content: boundedstore.StringSource("retired work")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.GrantDraft(f.ctx, d.ID, p.ID, time.Hour); e != nil {
		t.Fatal(e)
	}
	grant := f.query(t, contract.OwnerQuery{View: "draft_grants"}).Grants[0].Handle
	target := contract.Location{SystemID: f.c.SystemID(), ServiceID: "next", Endpoint: "https://next.test", Certificate: "fixture", Composition: "test"}
	h, e := f.c.PrepareHandoff(f.ctx, "retired-first", target)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.c.DecideHandoff(f.ctx, h.ID, h.Revision, true); e != nil {
		t.Fatal(e)
	}
	h, e = f.c.FreezeHandoff(f.ctx, h.ID, true)
	if e != nil {
		t.Fatal(e)
	}
	snapshot, e := f.s.Snapshot(f.ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.c.RetireHandoff(f.ctx, h.ID, snapshot.SHA256, h.Revision); e != nil {
		t.Fatal(e)
	}
	response, _ := f.request(t, "action", contract.OwnerAction{Action: "revoke_grant", Handle: grant}, nil)
	if response.StatusCode < 400 {
		t.Fatal("retired source reported revocation", response.StatusCode)
	}
}

func TestOwnerWindowSourceEditForgetAndGrantRevocation(t *testing.T) {
	f := ownerHTTP(t)
	asset, e := f.k.Create(f.ctx, contract.CreateInput{Content: "source evidence", Source: domain.Source{Ref: "local:fixture", Actor: "source-author"}})
	if e != nil {
		t.Fatal(e)
	}
	assets := f.query(t, contract.OwnerQuery{View: "assets"})
	if len(assets.Assets) != 1 {
		t.Fatal(assets)
	}
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Target: assets.Assets[0].Handle, Text: textPtr("owner current revision")})
	revised := f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "edit-original"})
	if f.query(t, contract.OwnerQuery{View: "original", Handle: revised.Handle}).Text.Text != "source evidence" {
		t.Fatal("original overwritten")
	}
	if f.query(t, contract.OwnerQuery{View: "content", Handle: revised.Handle}).Text.Text != "owner current revision" {
		t.Fatal("current edit not applied")
	}
	p, _, agent := f.agent(t, "grant-revocation")
	d = f.act(t, contract.OwnerAction{Action: "create_draft", Target: revised.Handle})
	var principal string
	for _, v := range f.query(t, contract.OwnerQuery{View: "connections"}).Connections {
		if v.Name == p.Name {
			principal = v.Handle
		}
	}
	g := f.act(t, contract.OwnerAction{Action: "grant_draft", Handle: d.Handle, Target: principal, Seconds: 3600})
	hostCall(t, agent, "ownward_draft_work", contract.AgentDraftRequest{Action: "read", Draft: g.Handle, Grant: g.Grant}, true)
	f.act(t, contract.OwnerAction{Action: "permissions", Handle: principal, OperationID: "revoke-draft-worker"})
	hostCall(t, agent, "ownward_draft_work", contract.AgentDraftRequest{Action: "read", Draft: g.Handle, Grant: g.Grant}, false)
	f.act(t, contract.OwnerAction{Action: "forget", Handle: revised.Handle, OperationID: "forget-window"})
	response, _ := f.request(t, "query", contract.OwnerQuery{View: "draft_content", Handle: d.Handle}, nil)
	if response.StatusCode < 400 {
		t.Fatal("forgotten target draft visible")
	}
	response, _ = f.request(t, "query", contract.OwnerQuery{View: "original", Handle: revised.Handle}, nil)
	if response.StatusCode < 400 {
		t.Fatal("forgotten original visible")
	}
	page := f.query(t, contract.OwnerQuery{View: "events"})
	for _, event := range page.Activity {
		if event.Asset != "" {
			t.Fatalf("forgotten asset reopens: %s", asset.Information.ID)
		}
	}
}

// Opt-in, isolated browser fixture. The entry contains only synthetic test
// credentials; the harness ends on the caller's stop file or its own deadline.
func TestOwnerWindowBrowserHarness(t *testing.T) {
	path := os.Getenv("OWNWARD_BROWSER_FIXTURE")
	if path == "" {
		t.Skip("manual browser verification harness")
	}
	f := ownerHTTP(t)
	entry := f.server.URL + ownerwindow.Prefix + "#" + f.bootstrap(t)
	data, _ := json.Marshal(map[string]string{"entry": entry, "origin": f.server.URL})
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	timer := time.NewTimer(3 * time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatal("browser verification did not finish")
		case <-ticker.C:
			if _, e := os.Stat(path + ".stop"); e == nil {
				return
			}
		}
	}
}

func TestOwnerWindowRelationshipsRemainGroundedAndRebuildHonest(t *testing.T) {
	f := ownerHTTP(t)
	var ids []string
	for _, text := range []string{"supporting original", "target original"} {
		a, e := f.k.Create(f.ctx, contract.CreateInput{Content: text})
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, a.Information.ID)
	}
	if e := f.s.CreateGeneration(f.ctx, "owner-test-generation", "fixture-space"); e != nil {
		t.Fatal(e)
	}
	if got := f.query(t, contract.OwnerQuery{View: "overview"}).Organization; got != "unavailable" {
		t.Fatal("no active generation represented as available", got)
	}
	for i, id := range ids {
		r := derived.Record{AssetID: id, AssetRevision: 1, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: "fixture"}}
		if i == 0 {
			r.Analysis.Organization = &semantics.Organization{Schema: semantics.OrganizationSchema, Snapshot: "fixture-organization", Links: []semantics.GroundedLink{{ID: "support", Type: "supports", Meaning: "The first original supports the second.", Source: semantics.GraphEndpoint{AssetID: id, Revision: 1}, Target: semantics.GraphEndpoint{AssetID: ids[1], Revision: 1}}}}
		}
		b, _ := json.Marshal(r)
		v, e := f.s.StageOrganization(f.ctx, "owner-test-generation", boundedstore.StringSource(b))
		if e != nil {
			t.Fatal(e)
		}
		if e = f.s.PublishOrganization(f.ctx, v); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.s.ActivateGeneration(f.ctx, "owner-test-generation", ""); e != nil {
		t.Fatal(e)
	}
	assets := f.query(t, contract.OwnerQuery{View: "assets", State: "ready"})
	if len(assets.Assets) != 2 {
		t.Fatal("organized assets shown pending", assets)
	}
	relations := f.query(t, contract.OwnerQuery{View: "relations", Handle: assets.Assets[0].Handle})
	if len(relations.Relations) != 1 {
		t.Fatal(relations)
	}
	relation := relations.Relations[0]
	if len(relation.Basis) != 2 || relation.Meaning == "" {
		t.Fatal("missing grounds", relation)
	}
	text := f.query(t, contract.OwnerQuery{View: "relation_text", Handle: relation.Meaning})
	var meaning string
	if e := json.Unmarshal([]byte(text.Text.Text), &meaning); e != nil || meaning != "The first original supports the second." {
		t.Fatal("meaning not reconstructable", text.Text, e)
	}
	for _, basis := range relation.Basis {
		if basis.StartRune != 0 || basis.EndRune == 0 {
			t.Fatal("bad source span", basis)
		}
		f.query(t, contract.OwnerQuery{View: "content", Handle: basis.Asset})
	}
	overview := f.query(t, contract.OwnerQuery{View: "overview"})
	if overview.Overview.Connected != 2 || !overview.Overview.Approximate {
		t.Fatal(overview.Overview)
	}
	if e := f.s.CreateGeneration(f.ctx, "new-owner-generation", "fixture-space"); e != nil {
		t.Fatal(e)
	}
	if got := f.query(t, contract.OwnerQuery{View: "overview"}).Organization; got != "rebuilding" {
		t.Fatal(got)
	}
}
