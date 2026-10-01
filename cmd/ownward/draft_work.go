package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/assembly"
	"github.com/HJSunDev/ownward/internal/contract"
)

// This is a public locator, not a credential. A copied request cannot read or
// write content until the owner approves the authenticated receiving principal.
type draftWorkLocator struct {
	System     string `json:"system"`
	DataDir    string `json:"data_dir"`
	Invitation string `json:"invitation"`
}

func draftInstruction(dataDir, system string) func(contract.DraftInvitation, string) string {
	return func(inv contract.DraftInvitation, instruction string) string {
		executable, e := os.Executable()
		if e != nil {
			return instruction
		}
		data, _ := json.Marshal(draftWorkLocator{System: system, DataDir: dataDir, Invitation: inv.ID})
		args, _ := json.Marshal([]string{"draft-work", "--invitation", base64.RawURLEncoding.EncodeToString(data), "--agent", "你的智能体名称", "--action", "request"})
		return instruction + fmt.Sprintf("\n\n若当前没有连接 Ownward 工具，可使用本机执行能力，无须用户配置：可执行文件 %q，参数数组 %s。首次调用只登记无资料权限的连接并申请本稿，凭据由程序保管。后续复用 invitation 和 agent，传 --action status --request <返回值> --wait 25 等待；获准后 --action read --request <返回值> 读取。修改使用 --action replace（或 append）--request <返回值> --handle <当前句柄> --text-file <UTF-8 文件>。不要循环申请，不要读取或展示任何凭据。没有工具连接或本机执行能力时，请明确告知无法接续，保留原文稿。", executable, args)
	}
}

func runDraftWork(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("draft-work", flag.ContinueOnError)
	f.SetOutput(stderr)
	locator := f.String("invitation", "", "文稿交接材料（不含授权）")
	agent := f.String("agent", "", "当前智能体名称，后续调用保持一致")
	action := f.String("action", "request", "request/status/read/replace/append")
	request := f.String("request", "", "协作申请标识")
	handle := f.String("handle", "", "读取返回的当前版本句柄")
	file := f.String("text-file", "", "修改文字的 UTF-8 文件")
	wait := f.Int("wait", 0, "等待批准的秒数，0 至 25")
	offset := f.Int64("offset", 0, "读取的 UTF-8 字节偏移")
	if e := f.Parse(args); e != nil {
		return e
	}
	if len(*locator) > 16384 || strings.TrimSpace(*agent) == "" || len(*agent) > 160 {
		return errors.New("请提供有效交接材料和智能体名称")
	}
	data, e := base64.RawURLEncoding.DecodeString(*locator)
	if e != nil {
		return errors.New("文稿交接材料无效")
	}
	var material draftWorkLocator
	if json.Unmarshal(data, &material) != nil || material.System == "" || material.Invitation == "" || !filepath.IsAbs(material.DataDir) {
		return errors.New("文稿交接材料无效")
	}
	identity, e := assembly.ReadLocalIdentity(ctx, material.DataDir)
	if e != nil || identity.InformationControl == nil || identity.InformationControl.SystemID != material.System {
		return errors.New("交接资料库暂不可用，请从原文稿重新发起；未切换或创建资料库")
	}
	bundle, e := currentVectorBundleDirectory()
	if e != nil {
		return e
	}
	verification, e := assembly.PreflightSharedConnector(assembly.Collaborative, bundle)
	if e != nil {
		return e
	}
	descriptor, e := ensureSharedMCPService(ctx, material.DataDir, version, verification.Composition, stderr)
	if e != nil {
		return e
	}
	h, e := newHostConnector(ctx, descriptor, material.DataDir)
	if e != nil {
		return e
	}
	if h.system != material.System {
		return errors.New("当前连接与交接资料库不符")
	}
	// A separate namespace prevents this convenience entry borrowing an MCP
	// connection's general permissions merely by repeating its display name.
	if e = h.initializeLocalPrincipal(ctx, "文稿协作 · "+strings.TrimSpace(*agent), h.owner); e != nil {
		return e
	}
	in := contract.AgentDraftRequest{Action: *action, Invitation: material.Invitation, System: material.System, Request: *request, Handle: *handle, WaitSeconds: *wait, Offset: *offset}
	if *file != "" {
		input, e := os.Open(*file)
		if e != nil {
			return e
		}
		defer input.Close()
		content, e := io.ReadAll(io.LimitReader(input, contract.OwnerRequestBytes+1))
		if e != nil {
			return e
		}
		if len(content) > contract.OwnerRequestBytes {
			return errors.New("本次文字超过提交上限，请分段追加")
		}
		in.Text = string(content)
	}
	var out contract.AgentDraftResult
	if e = h.controlCall(ctx, "draft-work", h.credential(), in, &out); e != nil {
		return e
	}
	return writeJSON(stdout, out)
}
