package rpcstream

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const EnvelopeBytes = 64 * 1024
const inputField = "_ownward_local_input"
const outputField = "_ownward_local_output"

// RPC身份沿用SDK的逻辑值；字符串转义和数值编码不构成不同调用。
func rpcIdentity(raw json.RawMessage) (jsonrpc.ID, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return jsonrpc.ID{}, err
	}
	id, err := jsonrpc.MakeID(value)
	if err != nil || !id.IsValid() {
		return jsonrpc.ID{}, errors.New("无效RPC调用身份")
	}
	return id, nil
}

func sameRPCIdentity(a, b json.RawMessage) bool {
	x, err := rpcIdentity(a)
	if err != nil {
		return false
	}
	y, err := rpcIdentity(b)
	return err == nil && x == y
}

// Scope 只属于一个已认证连接。引用不包含文件路径，不能跨连接解析。
type Scope struct {
	Dir       string
	Budget    *resourcebudget.Budget
	DiskBytes int64
	Disk      *resourcebudget.Disk
	mu        sync.Mutex
	calls     map[string]*Call
	closed    bool
}

type Call struct {
	ctx           context.Context
	cancel        context.CancelFunc
	Scope         *Scope
	Tool          string
	ID            json.RawMessage
	Arguments     streamjson.Node
	input, output *streamjson.Document
	outputNode    streamjson.Node
	token         string
	check         func() error
	once          sync.Once
	mu            sync.Mutex
	closed        bool
}

func New(dir string, budget *resourcebudget.Budget, diskBytes int64) *Scope {
	return &Scope{Dir: dir, Budget: budget, DiskBytes: diskBytes, Disk: resourcebudget.NewDisk(diskBytes), calls: map[string]*Call{}}
}

func (s *Scope) Close() error {
	s.mu.Lock()
	s.closed = true
	calls := make([]*Call, 0, len(s.calls))
	for _, c := range s.calls {
		calls = append(calls, c)
	}
	s.mu.Unlock()
	for _, c := range calls {
		c.Close()
	}
	return nil
}
func (c *Call) Close() {
	c.once.Do(func() {
		c.cancel()
		c.mu.Lock()
		defer c.mu.Unlock()
		c.closed = true
		c.Scope.mu.Lock()
		delete(c.Scope.calls, c.token)
		c.Scope.mu.Unlock()
		if c.output != nil {
			c.output.Close()
		}
		if c.input != nil {
			c.input.Close()
		}
	})
}

type limitBuffer struct{ bytes.Buffer }

func (b *limitBuffer) Write(p []byte) (int, error) {
	if len(p) > EnvelopeBytes-b.Len() {
		return 0, errors.New("RPC信封超过64KiB")
	}
	return b.Buffer.Write(p)
}
func literal(v any) func(io.Writer) error {
	return func(w io.Writer) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
}

// Project 在外部字节进入 SDK 前执行。外部同名字段始终作为原参数保存，不能取得内部引用。
func (s *Scope) Project(ctx context.Context, r io.Reader) ([]byte, *Call, error) {
	ctx = resourcebudget.WithDisk(ctx, s.Disk)
	ctx, cancel := context.WithCancel(ctx)
	kept := false
	defer func() {
		if !kept {
			cancel()
		}
	}()
	d, err := streamjson.Parse(ctx, s.Dir, r, s.Budget, s.DiskBytes)
	if err != nil {
		return nil, nil, err
	}
	fail := func(e error) ([]byte, *Call, error) { d.Close(); return nil, nil, e }
	root := d.Root()
	method, ok, err := root.Field("method")
	if err != nil {
		return fail(err)
	}
	var name string
	if ok {
		name, err = method.String(128)
		if err != nil {
			return fail(err)
		}
	}
	if name != "tools/call" {
		var b limitBuffer
		if err = root.Copy(&b); err != nil {
			return fail(err)
		}
		d.Close()
		return b.Bytes(), nil, nil
	}
	params, ok, err := root.Field("params")
	if err != nil || !ok {
		return fail(errors.New("缺少工具参数"))
	}
	tool, ok, err := params.Field("name")
	if err != nil || !ok {
		return fail(errors.New("缺少工具名"))
	}
	name, err = tool.String(256)
	if err != nil {
		return fail(err)
	}
	if !StorageTool(name) {
		var b limitBuffer
		if err = root.Copy(&b); err != nil {
			return fail(err)
		}
		d.Close()
		return b.Bytes(), nil, nil
	}
	args, ok, err := params.Field("arguments")
	if err != nil || !ok || args.Kind != '{' {
		return fail(errors.New("工具参数必须为对象"))
	}
	id, ok, err := root.Field("id")
	if err != nil || !ok {
		return fail(errors.New("工具调用缺少RPC身份"))
	}
	var rpcID json.RawMessage
	if err = id.DecodeSmall(&rpcID, 1024); err != nil {
		return fail(err)
	}
	if _, err = rpcIdentity(rpcID); err != nil {
		return fail(err)
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return fail(err)
	}
	c := &Call{ctx: ctx, cancel: cancel, Scope: s, Tool: name, ID: rpcID, Arguments: args, input: d, token: hex.EncodeToString(random[:])}
	var b limitBuffer
	err = root.Object(&b, map[string]func(io.Writer) error{"params": func(w io.Writer) error {
		return params.Object(w, map[string]func(io.Writer) error{"arguments": literal(map[string]string{inputField: c.token})})
	}})
	if err != nil {
		return fail(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.calls) >= 32 {
		return fail(errors.New("连接已关闭或在途调用超过工作区容量"))
	}
	for _, active := range s.calls {
		if sameRPCIdentity(active.ID, c.ID) {
			return fail(errors.New("RPC调用身份重复"))
		}
	}
	s.calls[c.token] = c
	kept = true
	return b.Bytes(), c, nil
}

func StorageTool(name string) bool {
	switch name {
	case "ownward_rules", "ownward_status", "ownward_check", "ownward_connections", "ownward_manage", "ownward_management_status", "ownward_create", "ownward_create_batch", "ownward_read", "ownward_update", "ownward_search", "ownward_navigate", "ownward_evidence_search", "ownward_evidence_read", "ownward_semantic_work", "ownward_semantic_submit", "ownward_semantic_submit_batch":
		return true
	}
	return false
}

func (c *Call) Context() context.Context { return c.ctx }

func (s *Scope) Resolve(tool string, args json.RawMessage) (*Call, error) {
	var ref map[string]string
	if len(args) > 512 || json.Unmarshal(args, &ref) != nil || len(ref) != 1 {
		return nil, errors.New("缺少本连接的参数引用")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.calls[ref[inputField]]
	if c == nil || c.Tool != tool {
		return nil, errors.New("参数引用已失效或不属于本调用")
	}
	return c, nil
}

func (c *Call) Digest(ctx context.Context) (string, error) {
	h := sha256.New()
	io.WriteString(h, c.Tool+":")
	if err := c.Arguments.Canonical(ctx, h); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Result 的 write 必须在返回前关闭数据库快照。check 在真实网络交付前再次核对权限与来源。
func (c *Call) Result(ctx context.Context, write func(io.Writer) error, check func() error) (*mcp.CallToolResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("调用已经关闭")
	}
	if c.output != nil {
		return nil, errors.New("调用已经产生结果")
	}
	d, err := streamjson.Build(resourcebudget.WithDisk(ctx, c.Scope.Disk), c.Scope.Dir, c.Scope.Budget, c.Scope.DiskBytes, write)
	if err != nil {
		return nil, err
	}
	c.output = d
	c.outputNode = d.RootContext(c.ctx)
	c.check = check
	return &mcp.CallToolResult{StructuredContent: map[string]string{outputField: c.token}, Content: []mcp.Content{&mcp.TextContent{Text: c.token}}}, nil
}

// Expand 仅展开本调用产生的结果；文本与结构化结果分别编码，不复制完整正文。
func (c *Call) Expand(ctx context.Context, w io.Writer, envelope []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("调用已经关闭")
	}
	if len(envelope) > EnvelopeBytes {
		return errors.New("SDK返回信封超过64KiB")
	}
	var rpc map[string]json.RawMessage
	if err := json.Unmarshal(envelope, &rpc); err != nil {
		return err
	}
	if !sameRPCIdentity(rpc["id"], c.ID) {
		return errors.New("结果RPC身份不匹配")
	}
	if c.output == nil {
		_, err := w.Write(envelope)
		return err
	}
	var result struct {
		StructuredContent map[string]string `json:"structuredContent"`
		Content           []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(rpc["result"], &result); err != nil {
		return err
	}
	if len(result.StructuredContent) != 1 || result.StructuredContent[outputField] != c.token {
		return errors.New("结果引用不属于本调用")
	}
	if len(result.Content) == 0 {
		return errors.New("缺少正文投影")
	}
	var first struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(result.Content[0], &first) != nil || first.Text != c.token {
		return errors.New("正文投影顺序不匹配")
	}
	if c.check != nil {
		if err := c.check(); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, `{"jsonrpc":"2.0","id":`); err != nil {
		return err
	}
	if _, err := w.Write(c.ID); err != nil {
		return err
	}
	if _, err := io.WriteString(w, `,"result":{"structuredContent":`); err != nil {
		return err
	}
	if err := c.outputNode.Copy(w); err != nil {
		return err
	}
	if _, err := io.WriteString(w, `,"content":[{"type":"text","text":`); err != nil {
		return err
	}
	if err := streamjson.WriteString(ctx, w, c.outputNode.Raw()); err != nil {
		return err
	}
	if _, err := io.WriteString(w, `}`); err != nil {
		return err
	}
	// 宿主追加的管理回执和结果元数据继续交付，不能因正文投影而丢失。
	for i, content := range result.Content {
		if i == 0 {
			var first struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(content, &first) != nil || first.Text != c.token {
				return errors.New("正文投影顺序不匹配")
			}
			continue
		}
		if _, err := io.WriteString(w, ","); err != nil {
			return err
		}
		if _, err := w.Write(content); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, "]"); err != nil {
		return err
	}
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(rpc["result"], &extra); err != nil {
		return err
	}
	for key, value := range extra {
		if key == "structuredContent" || key == "content" {
			continue
		}
		if _, err := io.WriteString(w, ","); err != nil {
			return err
		}
		encoded, _ := json.Marshal(key)
		if _, err := w.Write(encoded); err != nil {
			return err
		}
		if _, err := io.WriteString(w, ":"); err != nil {
			return err
		}
		if _, err := w.Write(value); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "}}")
	return err
}
