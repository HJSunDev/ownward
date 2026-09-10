package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/adapter/remote"
	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type remoteFixture struct {
	c        *informationcontrol.Control
	p        *informationcontrol.Product
	kernel   *core.Service
	identity remote.Identity
	client   *http.Client
	owner    string
}

func newRemoteFixture(t *testing.T) remoteFixture {
	t.Helper()
	a, err := authoritysubstrate.Open(filepath.Join(t.TempDir(), "data"), contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	kernel, err := core.NewWithAuthority(a.Assets())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { kernel.Close() })
	c := informationcontrol.New(a.Control())
	owner, err := c.InitializeOwner("owner")
	if err != nil {
		t.Fatal(err)
	}
	p := informationcontrol.NewProduct(kernel, c)
	t.Cleanup(p.Close)
	srv := httptest.NewUnstartedServer(nil)
	identity, err := remote.NewIdentity("https://"+srv.Listener.Addr().String(), "test")
	if err != nil {
		t.Fatal(err)
	}
	identity.Location.SystemID = c.SystemID()
	cert, err := identity.TLSCertificate()
	if err != nil {
		t.Fatal(err)
	}
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	srv.Config.Handler = remoteHandler(identity.Location, controlHTTPServer{server: mcpserver.New(p, "test"), control: c, product: p, kernel: kernel, generation: kernel.OperationGeneration})
	srv.StartTLS()
	t.Cleanup(srv.Close)
	client, err := remote.Client(identity.Location, nil)
	if err != nil {
		t.Fatal(err)
	}
	return remoteFixture{c, p, kernel, identity, client, owner}
}

func TestRemoteTransportRejectsWrongTrustAndLocalRecovery(t *testing.T) {
	f := newRemoteFixture(t)
	ctx := context.Background()
	var identity any
	if err := remoteCall(ctx, f.client, f.identity.Location, "/remote/identity", "", nil, &identity); err != nil {
		t.Fatal(err)
	}
	other, err := remote.NewIdentity(f.identity.Location.Endpoint, "test")
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := remote.Client(other.Location, nil)
	if err != nil {
		t.Fatal(err)
	}
	if remoteCall(ctx, wrong, other.Location, "/remote/identity", "", nil, &identity) == nil {
		t.Fatal("untrusted service certificate accepted")
	}
	for _, path := range []string{controlPrefix + "recover", controlPrefix + "enroll", sharedMCPShutdownPath, sharedMCPStatusPath} {
		if remoteCall(ctx, f.client, f.identity.Location, path, f.owner, struct{}{}, &identity) == nil {
			t.Fatal("local capability exposed remotely", path)
		}
	}
	request, _ := http.NewRequest("GET", f.identity.Location.Endpoint+controlPrefix+"self", nil)
	request.Header.Set(principalHeader, f.owner)
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode == 200 {
		t.Fatal("self-declared internal identity accepted")
	}
}

func TestRemoteMCPUsesSameRulesAndResumesMutation(t *testing.T) {
	f := newRemoteFixture(t)
	ctx := context.Background()
	owner := informationcontrol.Authenticate(ctx, f.owner)
	p, token, err := f.c.Enroll(owner, "reader")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.SetPermissions(owner, p.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission}); err != nil {
		t.Fatal(err)
	}
	client := *f.client
	client.Transport = remoteBearer{base: client.Transport, credential: func() string { return token }}
	c := mcp.NewClient(&mcp.Implementation{Name: "remote-test"}, nil)
	session, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: f.identity.Location.Endpoint + "/capabilities", HTTPClient: &client, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	params := &mcp.CallToolParams{Meta: mcp.Meta{"ownward/operation": "same-request", "ownward/generation": 1}, Name: "ownward_create", Arguments: map[string]any{"content": "user owned information"}}
	first, err := session.CallTool(ctx, params)
	if err != nil || first.IsError {
		t.Fatal("first mutation failed", first, err)
	}
	second, err := session.CallTool(ctx, params)
	if err != nil || second.IsError {
		t.Fatal("retry mutation failed", second, err)
	}
	var a, b mcpserver.CreateOutput
	if err := decodeTool(first, &a); err != nil {
		t.Fatal(err)
	}
	if err := decodeTool(second, &b); err != nil {
		t.Fatal(err)
	}
	dataA, _ := json.Marshal(a)
	dataB, _ := json.Marshal(b)
	if string(dataA) != string(dataB) {
		t.Fatal("same operation returned different asset", string(dataA), string(dataB))
	}
	params.Arguments = map[string]any{"content": "different input"}
	changed, err := session.CallTool(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if !changed.IsError {
		t.Fatal("same operation could change content")
	}
	if err := f.c.SetPermissions(owner, p.ID, nil); err != nil {
		t.Fatal(err)
	}
	params.Arguments = map[string]any{"content": "user owned information"}
	denied, err := session.CallTool(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if !denied.IsError {
		t.Fatal("replay bypassed revocation")
	}
}

func TestRemoteEnrollmentSurvivesConnectorReplacement(t *testing.T) {
	f := newRemoteFixture(t)
	ctx := context.Background()
	owner := informationcontrol.Authenticate(ctx, f.owner)
	id := "durable-invitation"
	_, err := f.c.Invite(owner, id)
	if err != nil {
		t.Fatal(err)
	}
	material := connectionMaterial{Location: f.identity.Location, EnrollmentID: id}
	vault := localowner.Vault{Root: t.TempDir()}
	host, err := newRemoteHost(ctx, material, vault)
	if err != nil {
		t.Fatal(err)
	}
	host.record.JoinProof = strings.Repeat("proof", 12)
	if err := host.save(); err != nil {
		t.Fatal(err)
	}
	_, err = f.c.Join(id, host.record.JoinProof, "same-name", []contract.Permission{contract.ReadPermission})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.DecideEnrollment(owner, id, informationcontrol.EnrollmentMarker(id, host.record.JoinProof), true); err != nil {
		t.Fatal(err)
	}
	e, token, err := f.c.ClaimEnrollment(id, host.record.JoinProof)
	if err != nil {
		t.Fatal(err)
	}
	host.record.Credential = token
	host.record.Principal = e.Principal
	if err := host.save(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newRemoteHost(ctx, material, vault)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.record.Principal != e.Principal || reloaded.credential() != token {
		t.Fatal("restarting connector lost identity")
	}
}

func (f remoteFixture) hostSession(t *testing.T, h *hostConnector, name string, approve bool) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	client := *f.client
	client.Transport = remoteBearer{base: client.Transport, credential: h.credential}
	upstream := mcp.NewClient(&mcp.Implementation{Name: "connector"}, nil)
	up, err := upstream.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: f.identity.Location.Endpoint + "/capabilities", HTTPClient: &client, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { up.Close() })
	proxy := mcp.NewServer(&mcp.Implementation{Name: "ownward"}, nil)
	for tool, err := range up.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		proxy.AddTool(tool, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if err := h.initialize(ctx, r); err != nil {
				return nil, err
			}
			if err := h.processEnrollments(ctx, r.Session); err != nil {
				return nil, err
			}
			return h.call(ctx, r, up)
		})
	}
	a, b := mcp.NewInMemoryTransports()
	server, err := proxy.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	external := mcp.NewClient(&mcp.Implementation{Name: name}, &mcp.ClientOptions{ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": approve}}, nil
	}})
	session, err := external.Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestRemoteHostApprovalContinuesTheOriginalCreate(t *testing.T) {
	f := newRemoteFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner := informationcontrol.Authenticate(ctx, f.owner)
	manager, token, err := f.c.Enroll(owner, "trusted host")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.c.SetPermissions(owner, manager.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission, contract.ManagePermission}); err != nil {
		t.Fatal(err)
	}
	managerHost, err := newRemoteHost(ctx, connectionMaterial{Location: f.identity.Location, EnrollmentID: "manager-profile"}, localowner.Vault{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	managerHost.record = connectorRecord{Credential: token, Principal: manager.ID, Connected: true}
	if err := managerHost.save(); err != nil {
		t.Fatal(err)
	}
	managerSession := f.hostSession(t, managerHost, "trusted host", true)
	_, err = f.c.Invite(informationcontrol.Authenticate(ctx, token), "new-device")
	if err != nil {
		t.Fatal(err)
	}
	targetHost, err := newRemoteHost(ctx, connectionMaterial{Location: f.identity.Location, EnrollmentID: "new-device"}, localowner.Vault{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	targetSession := f.hostSession(t, targetHost, "new device", false)
	type answer struct {
		result *mcp.CallToolResult
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		r, err := targetSession.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_create", Arguments: map[string]string{"content": "resume the original task"}})
		done <- answer{r, err}
	}()
	for {
		entries, err := f.c.Enrollments(informationcontrol.Authenticate(ctx, token))
		if err != nil {
			t.Fatal(err)
		}
		pending := false
		for _, e := range entries {
			pending = pending || e.Status == "pending"
		}
		if pending {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("target did not enroll")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if r, err := managerSession.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_rules", Arguments: struct{}{}}); err != nil || r.IsError {
		t.Fatal("trusted host did not complete confirmation", r, err)
	}
	select {
	case a := <-done:
		if a.err != nil || a.result.IsError {
			t.Fatal("original task did not resume", a.result, a.err)
		}
		var output mcpserver.CreateOutput
		if err := decodeTool(a.result, &output); err != nil {
			t.Fatal(err)
		}
		if targetHost.credential() == "" {
			t.Fatal("target did not privately retain credential")
		}
	case <-ctx.Done():
		t.Fatal("approved task remained blocked")
	}
}
