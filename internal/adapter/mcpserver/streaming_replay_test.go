package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type streamReplayFixture struct {
	t       *testing.T
	ctx     context.Context
	session *mcp.ClientSession
	store   *boundedstore.Store
}

func newStreamReplayFixture(t *testing.T, vectors ...contract.VectorCapability) *streamReplayFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	root := t.TempDir()
	budget, _ := resourcebudget.New(12*resourcebudget.MiB, resourcebudget.MiB)
	store, err := boundedstore.Open(ctx, filepath.Join(root, "ownward.sqlite"), boundedstore.Options{Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	digest := strings.Repeat("a", 64)
	principal := contract.Principal{ID: "review", Revision: 1, CredentialDigest: digest, Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission}}
	if err = store.PublishAccess(ctx, boundedstore.AccessHeader{System: "review-system", Revision: 1}, 0, []contract.Principal{principal}); err != nil {
		t.Fatal(err)
	}
	service := &core.StreamingAssets{Store: store, Budget: budget, Scratch: root, DiskBytes: 32 * resourcebudget.MiB}
	if len(vectors) > 0 {
		service.Embedder = vectors[0]
	}
	server := NewStreamingStorage(service, "round2", root, budget, 32*resourcebudget.MiB)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.HTTPHandler().ServeHTTP(w, r.WithContext(contract.WithAuthenticationDigest(r.Context(), digest)))
	}))
	t.Cleanup(host.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "review", Version: "2"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: host.URL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return &streamReplayFixture{t, ctx, session, store}
}
func (f *streamReplayFixture) call(name, op string, args any) *mcp.CallToolResult {
	f.t.Helper()
	out, err := f.session.CallTool(f.ctx, &mcp.CallToolParams{Name: name, Arguments: args, Meta: mcp.Meta{"ownward/operation": op, "ownward/generation": 1}})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}
func (f *streamReplayFixture) decode(out *mcp.CallToolResult, value any) {
	f.t.Helper()
	if out.IsError {
		data,_:=json.Marshal(out.Content);f.t.Fatalf("unexpected tool error: %s",data)
	}
	raw, err := json.Marshal(out.StructuredContent)
	if err != nil {
		f.t.Fatal(err)
	}
	if err = json.Unmarshal(raw, value); err != nil {
		f.t.Fatal(err)
	}
}

func TestReadBasisPreservesOriginalFingerprintForPageBreak(t *testing.T) {
	f := newStreamReplayFixture(t)
	var created CreateOutput
	f.decode(f.call("ownward_create", "create", map[string]any{"content": "page one\fpage two"}), &created)
	var read ReadOutput
	f.decode(f.call("ownward_read", "", map[string]any{"id": created.Result.Information.ID}), &read)
	if read.Information.Content != "page one\fpage two" {
		t.Fatal("content changed")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(read.Basis, "b1-"))
	if err != nil {
		t.Fatal(err)
	}
	var basis struct {
		Content string `json:"c"`
	}
	if err = json.Unmarshal(raw, &basis); err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(read.Information)
	sum := sha256.Sum256(canonical)
	want := hex.EncodeToString(sum[:])
	if basis.Content != want {
		t.Fatalf("freshly read unchanged information has wrong basis: got %s original %s", basis.Content, want)
	}
}

func TestBatchReplayKeepsUnaffectedItemAfterOtherItemUpdated(t *testing.T) {
	f := newStreamReplayFixture(t)
	args := map[string]any{"items": []any{map[string]any{"content": "first original"}, map[string]any{"content": "second original"}, map[string]any{"content": "  "}}}
	var created CreateBatchOutput
	f.decode(f.call("ownward_create_batch", "batch", args), &created)
	first := created.Results[0].Result.Information
	var updated UpdateOutput
	f.decode(f.call("ownward_update", "update", map[string]any{"id": first.ID, "expected_revision": 1, "content": "first corrected"}), &updated)
	replay := f.call("ownward_create_batch", "batch", args)
	if replay.IsError {
		for _, c := range replay.Content {
			if text, ok := c.(*mcp.TextContent); ok {
				t.Logf("replay error: %s", text.Text)
			}
		}
		t.Fatal("one changed item suppresses the unchanged item's committed result")
	}
	var recovered CreateBatchOutput
	f.decode(replay, &recovered)
	if len(recovered.Results) != 3 || recovered.Results[0].Error != "操作已提交，原结果已更新或遗忘" || recovered.Results[1].Result == nil || recovered.Results[1].Result.Information.ID != created.Results[1].Result.Information.ID {
		t.Fatalf("wrong per-item recovery: %+v", recovered)
	}
	if recovered.Results[2].Error != created.Results[2].Error {
		t.Fatal("original invalid item changed")
	}
	if recovered.Results[1].Result.Information.Content != "second original" {
		t.Fatal("unaffected content changed")
	}
	page, err := f.store.ScanAssets(f.ctx, "", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatal("replay created duplicate assets")
	}
	second := created.Results[1].Result.Information
	f.decode(f.call("ownward_update", "update-second", map[string]any{"id": second.ID, "expected_revision": 1, "content": "second corrected"}), &updated)
	f.decode(f.call("ownward_create_batch", "batch", args), &recovered)
	if len(recovered.Results) != 3 || recovered.Results[0].Error == "" || recovered.Results[1].Error == "" {
		t.Fatal("all superseded items were not returned individually")
	}
	if err = f.store.PublishAccess(f.ctx, boundedstore.AccessHeader{System: "review-system", Revision: 2}, 1, []contract.Principal{{ID: "review", Revision: 2, CredentialDigest: strings.Repeat("a", 64)}}); err != nil {
		t.Fatal(err)
	}
	if !f.call("ownward_create_batch", "batch", args).IsError {
		t.Fatal("revoked authority hidden by per-item recovery")
	}

}
