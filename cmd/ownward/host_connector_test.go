package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type hostFixture struct {
	control    *informationcontrol.Control
	product    *informationcontrol.Product
	descriptor *sharedMCPDescriptor
	root       string
}

func newHostFixture(t *testing.T) hostFixture {
	t.Helper()
	configuration := t.TempDir()
	t.Setenv("APPDATA", configuration)
	t.Setenv("XDG_CONFIG_HOME", configuration)
	root := filepath.Join(t.TempDir(), "data")
	a, err := authoritysubstrate.Open(root, contract.ControlState{Schema: contract.ControlStateSchema, Revision: 1, ActiveComposition: "test", ActiveKernelGeneration: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	kernel, err := core.NewWithAuthority(a.Assets())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kernel.Close() })
	c := informationcontrol.New(a.Control())
	p := informationcontrol.NewProduct(kernel, c)
	t.Cleanup(p.Close)
	service := controlHTTPServer{server: mcpserver.New(p, "test"), control: c, product: p, kernel: kernel}
	if err := service.prepareRecovery(ownerRecoveryScope(root)); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(bearerTokenHandler(service.HTTPHandler(), "transport-only-test-token"))
	t.Cleanup(httpServer.Close)
	return hostFixture{c, p, &sharedMCPDescriptor{Endpoint: httpServer.URL, BearerToken: "transport-only-test-token"}, root}
}

// 协议自动化回归；模拟决定不作为真实用户确认验收证据。
func (f hostFixture) connect(t *testing.T, name string, approve func(string) bool) (*mcp.ClientSession, *hostConnector) {
	t.Helper()
	ctx := context.Background()
	h, err := newHostConnector(ctx, f.descriptor, f.root)
	if err != nil {
		t.Fatal(err)
	}
	upstream := mcp.NewClient(&mcp.Implementation{Name: "connector"}, nil)
	httpClient := &http.Client{Transport: bearerTransport{token: f.descriptor.BearerToken, credential: h.credential, base: http.DefaultTransport}}
	us, err := upstream.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: f.descriptor.Endpoint, HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = us.Close() })
	proxy := mcp.NewServer(&mcp.Implementation{Name: "ownward"}, nil)
	h.addMaterialTool(proxy, func(ctx context.Context, refs []string) ([]contract.InformationCheck, error) {
		result, err := us.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_check", Arguments: map[string]any{"bases": refs}})
		if err != nil {
			return nil, err
		}
		var out struct {
			Results []contract.InformationCheck `json:"results"`
		}
		err = decodeTool(result, &out)
		return out.Results, err
	})
	for tool, err := range us.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		proxy.AddTool(tool, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return h.call(ctx, r, us)
		})
	}
	ct, st := mcp.NewInMemoryTransports()
	ss, err := proxy.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	options := &mcp.ClientOptions{}
	if approve != nil {
		options.ElicitationHandler = func(_ context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			accepted := approve(r.Params.Message)
			action := "decline"
			if accepted {
				action = "accept"
			}
			return &mcp.ElicitResult{Action: action, Content: map[string]any{"confirm": accepted}}, nil
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: name}, options)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, h
}

func hostCall(t *testing.T, s *mcp.ClientSession, name string, input any, wantSuccess bool) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
	success := err == nil && result != nil && !result.IsError
	if success != wantSuccess {
		if result != nil {
			for _, c := range result.Content {
				if v, ok := c.(*mcp.TextContent); ok {
					t.Log(v.Text)
				}
			}
		}
		t.Fatalf("%s success=%v want=%v error=%v content=%#v", name, success, wantSuccess, err, result.Content)
	}
	return result
}

func TestHostBootstrapApprovalReconnectRevocationAndRecovery(t *testing.T) {
	f := newHostFixture(t)
	confirmations := 0
	session, h := f.connect(t, "daily-agent", func(string) bool { confirmations++; return true })
	result := hostCall(t, session, "ownward_create", map[string]any{"content": "isolated forget fixture"}, true)
	if confirmations != 2 {
		t.Fatalf("bootstrap and access confirmations=%d", confirmations)
	}
	var output struct {
		Result contract.MutationResult `json:"result"`
	}
	if err := decodeTool(result, &output); err != nil {
		t.Fatal(err)
	}
	value := output.Result
	owner, _ := h.owner()
	ownerCtx := informationcontrol.Authenticate(context.Background(), owner)
	if _, err := f.control.Principals(informationcontrol.Authenticate(context.Background(), h.credential())); err == nil {
		t.Fatal("ordinary agent became manager")
	}
	system, principal := h.system, h.record.Principal
	again, h2 := f.connect(t, "daily-agent", func(string) bool { t.Error("reconnect unexpectedly asked"); return false })
	hostCall(t, again, "ownward_read", map[string]any{"id": value.Information.ID}, true)
	if h2.record.Principal != principal || h2.system != system {
		t.Fatal("reconnect changed identities")
	}
	other, _ := f.connect(t, "guest-agent", func(string) bool { return false })
	hostCall(t, other, "ownward_read", map[string]any{"id": value.Information.ID}, false)
	if err := f.control.SetPermissions(ownerCtx, principal, nil); err != nil {
		t.Fatal(err)
	}
	before := confirmations
	hostCall(t, session, "ownward_read", map[string]any{"id": value.Information.ID}, false)
	if confirmations != before {
		t.Fatal("revocation silently requested reactivation")
	}
	// 被撤销的连接仍可向用户申请单次管理，不能自行管理。
	if err := h.vault.Save(system, "owner", "invalid-local-owner-fixture"); err != nil {
		t.Fatal(err)
	}
	request := contract.ManagementRequest{ID: "forget-fixture", Operation: "forget", Targets: []contract.AssetVersion{{ID: value.Information.ID, Revision: 1}}}
	result = hostCall(t, session, "ownward_manage", request, true)
	var op contract.ManagementReceipt
	if err := decodeTool(result, &op); err != nil {
		t.Fatal(err)
	}
	if op.Status != "cleaning" && op.Status != "completed" {
		t.Fatalf("forget state=%s", op.Status)
	}
	if confirmations != before+2 {
		t.Fatalf("recovery and operation confirmations=%d", confirmations-before)
	}
	if h.system != system || h.record.Principal != principal {
		t.Fatal("owner recovery changed user/agent identity")
	}
	if _, _, err := f.control.Begin(ownerCtx, contract.ManagePermission); err == nil {
		t.Fatal("old owner credential still works")
	}
	hostCall(t, session, "ownward_manage", request, true)
	if confirmations != before+2 {
		t.Fatal("retry required duplicate approval")
	}
}

func TestHostDeclineAndUnsupportedConfirmationCannotMutate(t *testing.T) {
	f := newHostFixture(t)
	noUI, _ := f.connect(t, "no-ui", nil)
	hostCall(t, noUI, "ownward_create", map[string]any{"content": "must not be stored"}, false)
	if f.control.SystemID() != "" {
		t.Fatal("first model request claimed ownership")
	}
	accepted := 0
	declined, h := f.connect(t, "declining-host", func(message string) bool {
		if strings.Contains(message, "建立属于") {
			accepted++
			return true
		}
		return false
	})
	hostCall(t, declined, "ownward_create", map[string]any{"content": "must not be stored"}, false)
	if accepted != 1 {
		t.Fatal("bootstrap was not shown")
	}
	owner, _ := h.owner()
	values, err := f.product.Search(informationcontrol.Authenticate(context.Background(), owner), contract.SearchInput{Query: "stored", Limit: 10})
	if err != nil || len(values) != 0 {
		t.Fatal("decline wrote data", err)
	}
	if err := h.controlCall(context.Background(), "recover", f.descriptor.BearerToken, struct{}{}, &struct{}{}); err == nil {
		t.Fatal("transport token became recovery proof")
	}
}
