package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStreamingStorageActualHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	budget, _ := resourcebudget.New(12*resourcebudget.MiB, resourcebudget.MiB)
	store, err := boundedstore.Open(ctx, filepath.Join(root, "ownward.sqlite"), boundedstore.Options{Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	credential := "unit-one-test-principal"
	hash := sha256.Sum256([]byte(credential))
	principal := contract.Principal{ID: "p", Revision: 1, CredentialDigest: hex.EncodeToString(hash[:]), Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission}}
	if err = store.PublishAccess(ctx, boundedstore.AccessHeader{System: "system", Revision: 1}, 0, []contract.Principal{principal}); err != nil {
		t.Fatal(err)
	}
	service := &core.StreamingAssets{Store: store, Budget: budget, Scratch: root, DiskBytes: 64 * resourcebudget.MiB}
	server := NewStreamingStorage(service, "unit-one", root, budget, 64*resourcebudget.MiB)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.HTTPHandler().ServeHTTP(w, r.WithContext(informationcontrol.Authenticate(r.Context(), r.Header.Get("X-Ownward-Principal"))))
	}))
	defer httpServer.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "external-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL, HTTPClient: &http.Client{Transport: streamTestAuth{credential: credential}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name, operation string, args any) *mcp.CallToolResult {
		t.Helper()
		result, e := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args, Meta: mcp.Meta{"ownward/operation": operation, "ownward/generation": 1}})
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	decode := func(result *mcp.CallToolResult, out any) {
		t.Helper()
		if result.IsError {
			t.Fatalf("工具失败: %+v", result.Content)
		}
		b, e := json.Marshal(result.StructuredContent)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(b, out); e != nil {
			t.Fatal(e)
		}
	}
	content := strings.Repeat("保留原文：中文、引号\"、换行\n、反斜线\\。", 20000) + "末尾唯一的附加说明。\b\f"
	args := CreateInput{Content: content, Contexts: []domain.Context{{Key: " 项目 ", Value: " A "}, {Key: "项目", Value: "a"}}, Source: domain.Source{Ref: "report"}}
	var created CreateOutput
	decode(call("ownward_create", "op-create", args), &created)
	if created.Result.Information.Content != content || len(created.Result.Information.Contexts) != 1 || created.Result.Information.Contexts[0].Value != "A" {
		t.Fatal("原文或场景归一化发生变化")
	}
	var retry CreateOutput
	decode(call("ownward_create", "op-create", args), &retry)
	if retry.Result.Information.ID != created.Result.Information.ID {
		t.Fatal("重复操作创建了新资产")
	}
	changed := args
	changed.Content = "different"
	if !call("ownward_create", "op-create", changed).IsError {
		t.Fatal("同操作更换正文未被拒绝")
	}
	var read ReadOutput
	decode(call("ownward_read", "", ReadInput{ID: created.Result.Information.ID}), &read)
	if read.Information.Content != content || len(read.Clarifications) != 0 {
		t.Fatal("读回结果不一致")
	}
	basis, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(read.Basis, "b1-"))
	if err != nil {
		t.Fatal(err)
	}
	var identity struct {
		Content string `json:"c"`
	}
	json.Unmarshal(basis, &identity)
	encoded, _ := json.Marshal(read.Information)
	expected := sha256.Sum256(encoded)
	if identity.Content != hex.EncodeToString(expected[:]) {
		t.Fatal("来源指纹不等同于原资产JSON")
	}
	updated := "更新后仍是完整原文。"
	var update UpdateOutput
	decode(call("ownward_update", "op-update", UpdateInput{ID: read.Information.ID, ExpectedRevision: 1, Content: &updated}), &update)
	if update.Result.Information.Revision != 2 || update.Result.Information.Content != updated {
		t.Fatal("更新失败")
	}
	if !call("ownward_update", "op-stale", UpdateInput{ID: read.Information.ID, ExpectedRevision: 1, Content: &updated}).IsError {
		t.Fatal("过期修订覆盖成功")
	}
	var batch CreateBatchOutput
	decode(call("ownward_create_batch", "op-batch", CreateBatchInput{Items: []CreateInput{{Content: "有效资料"}, {Content: "  "}}}), &batch)
	if len(batch.Results) != 2 || batch.Results[0].Result == nil || batch.Results[1].Error == "" {
		t.Fatal("批量逐项结果丢失")
	}
	var note CreateOutput
	selectors := []*domain.TextSelector{{Exact: "项目甲"}, {Exact: "仅适用于", Prefix: "资料"}}
	decode(call("ownward_create", "op-note", map[string]any{"content": "这份资料仅适用于项目甲。\b\f", "source": map[string]string{"actor": "", "ref": ""}, "explicit_relations": []domain.ExplicitRelation{{Type: "qualifies", TargetID: read.Information.ID, Selector: selectors[0]}, {Type: "qualifies", TargetID: read.Information.ID, Selector: selectors[1]}}}), &note)
	decode(call("ownward_read", "", ReadInput{ID: read.Information.ID}), &read)
	if len(read.Clarifications) != 2 || read.Clarifications[0].SourceID != note.Result.Information.ID || read.Clarifications[0].Evidence == nil || read.Clarifications[0].Covered {
		t.Fatal("明确说明未随来源交付", read.Clarifications)
	}
	var fingerprints []string
	data, _ := json.Marshal(note.Result.Information)
	noteHash := sha256.Sum256(data)
	for _, selector := range selectors {
		b, _ := json.Marshal(struct {
			ID, Hash string
			Selector *domain.TextSelector
		}{note.Result.Information.ID, hex.EncodeToString(noteHash[:]), selector})
		fingerprints = append(fingerprints, string(b))
	}
	sort.Strings(fingerprints)
	data, _ = json.Marshal(fingerprints)
	expectedNotes := sha256.Sum256(data)
	encodedBasis, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(read.Basis, "b1-"))
	var notesIdentity struct {
		Notes string `json:"n"`
	}
	json.Unmarshal(encodedBasis, &notesIdentity)
	if notesIdentity.Notes != hex.EncodeToString(expectedNotes[:]) {
		t.Fatal("说明依据身份与原实现不一致")
	}
	principal.Revision = 2
	principal.Permissions = nil
	if err = store.PublishAccess(ctx, boundedstore.AccessHeader{System: "system", Revision: 2}, 1, []contract.Principal{principal}); err != nil {
		t.Fatal(err)
	}
	if !call("ownward_read", "", ReadInput{ID: read.Information.ID}).IsError {
		t.Fatal("撤销后仍可读取")
	}
}

type streamTestAuth struct{ credential string }

func (t streamTestAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	request := r.Clone(r.Context())
	request.Header.Set("X-Ownward-Principal", t.credential)
	return http.DefaultTransport.RoundTrip(request)
}
