package main

import (
	"encoding/json"
	"fmt"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/hostmaterials"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexLocationSelectionPreservesValidSetupOnError(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	location := newRemoteFixture(t).identity.Location
	data, _ := json.Marshal(connectionMaterial{Location: location})
	source := filepath.Join(root, "public.json")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := configureCodex([]string{"--connection", source}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	path, _ := codexConnectionPath()
	if err := configureCodex([]string{"--connection", source + "-missing"}, io.Discard, io.Discard); err == nil {
		t.Fatal("无效位置覆盖成功")
	}
	got, err := readMaterial(path)
	if err != nil || got.Location != location {
		t.Fatal(got, err)
	}
	if err := configureCodex(nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("未切回本机")
	}
}

func TestHostCheckBatchWaitingCost(t *testing.T) {
	f := newHostFixture(t)
	s, _ := f.connect(t, "cost-sample", func(string) bool { return true })
	refs := make([]string, 0, 32)
	for i := 0; i < 32; i++ {
		v := hostCall(t, s, "ownward_create", map[string]any{"content": fmt.Sprintf("资料%d。%s", i, strings.Repeat("用户信息。", 400))}, true)
		var created struct {
			Result contract.MutationResult `json:"result"`
		}
		if err := decodeTool(v, &created); err != nil {
			t.Fatal(err)
		}
		var read contract.InformationRead
		if err := decodeTool(hostCall(t, s, "ownward_read", map[string]string{"id": created.Result.Information.ID}, true), &read); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, read.Basis)
	}
	start := time.Now()
	for i := 0; i < 20; i++ {
		var checks struct {
			Results []contract.InformationCheck `json:"results"`
		}
		if err := decodeTool(hostCall(t, s, "ownward_check", map[string]any{"bases": refs}, true), &checks); err != nil {
			t.Fatal(err)
		}
		if len(checks.Results) != 32 {
			t.Fatal("遗漏核对")
		}
		for _, c := range checks.Results {
			if c.Status != "unchanged" {
				t.Fatal(c)
			}
		}
	}
	t.Logf("32项 MCP 批量核对，20轮平均 %.3f ms；没有外部模型调用", float64(time.Since(start).Microseconds())/20000)
}

func TestHostMaterialsContinueOnCurrentAuthorizationWithoutNewApproval(t *testing.T) {
	f := newHostFixture(t)
	t.Setenv("OWNWARD_HOST_STATE", filepath.Join(t.TempDir(), "materials"))
	approvals := 0
	s, _ := f.connect(t, "native-codex", func(string) bool { approvals++; return true })
	value := hostCall(t, s, "ownward_create", map[string]any{"content": "我一直住北京。"}, true)
	var c struct {
		Result contract.MutationResult `json:"result"`
	}
	if err := decodeTool(value, &c); err != nil {
		t.Fatal(err)
	}
	read := hostCall(t, s, "ownward_read", map[string]string{"id": c.Result.Information.ID}, true)
	var r contract.InformationRead
	if err := decodeTool(read, &r); err != nil {
		t.Fatal(err)
	}
	hostCall(t, s, "ownward_host_event", map[string]any{"event": "PostToolUse", "session_id": "session-one", "tool_name": "mcp__ownward__ownward_read", "tool_response": read}, true)
	hostCall(t, s, "ownward_update", map[string]any{"id": r.Information.ID, "expected_revision": 1, "content": "此前住北京，2026年9月搬到杭州。"}, true)
	before := approvals
	output := hostCall(t, s, "ownward_host_event", map[string]any{"event": "UserPromptSubmit", "session_id": "session-one", "turn_id": "t2"}, true)
	var hook hostHookOutput
	if err := decodeTool(output, &hook); err != nil {
		t.Fatal(err)
	}
	if hook.Specific == nil || !strings.Contains(hook.Specific.Context, `"status":"changed"`) || !strings.Contains(hook.Specific.Context, r.Basis) || strings.Contains(hook.Specific.Context, "搬到杭州") {
		t.Fatal("失效状态或原文边界错误", hook)
	}
	if approvals != before {
		t.Fatal("自动核对重新申请授权")
	}
	// 相同宿主身份恢复，沿用接入的材料，不升级成所有者。
	resumed, h := f.connect(t, "native-codex", func(string) bool { t.Fatal("恢复询问授权"); return false })
	output = hostCall(t, resumed, "ownward_host_event", map[string]any{"event": "SessionStart", "source": "resume", "session_id": "session-one"}, true)
	if err := decodeTool(output, &hook); err != nil || hook.Specific == nil || !strings.Contains(hook.Specific.Context, `"status":"changed"`) {
		t.Fatal(hook, err)
	}
	if h.record.Principal == "" {
		t.Fatal("未恢复连接主体")
	}
	read = hostCall(t, resumed, "ownward_read", map[string]string{"id": r.Information.ID}, true)
	hostCall(t, resumed, "ownward_host_event", map[string]any{"event": "PostToolUse", "session_id": "session-one", "tool_name": "mcp__ownward__ownward_read", "tool_response": read}, true)
	output = hostCall(t, resumed, "ownward_host_event", map[string]any{"event": "SessionStart", "source": "clear", "session_id": "session-one"}, true)
	if err := decodeTool(output, &hook); err != nil {
		t.Fatal(err)
	}
}

func TestHookDoesNotTrackReasoningBasisOrOtherTools(t *testing.T) {
	state := hostmaterials.WorkingSet{Version: 1}
	data := json.RawMessage(`{"structuredContent":{"basis":{"established":"not a source"}}}`)
	if err := observeMaterial(&state, hostMaterialEvent{Tool: "mcp__other__analyze", Response: data}); err != nil {
		t.Fatal(err)
	}
	if len(state.Materials) != 0 {
		t.Fatal("思考字段被当成来源引用")
	}
}
