package codexplugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const organizationMessageBytes = 8 << 20

type boundedReader struct {
	r    io.Reader
	left int64
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, errors.New("组织输入超过边界")
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n, e := r.r.Read(p)
	r.left -= int64(n)
	return n, e
}

type organizationMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}
type organizationClient struct {
	command   *exec.Cmd
	input     io.WriteCloser
	output    io.ReadCloser
	messages  chan organizationMessage
	done      chan struct{}
	cancel    context.CancelFunc
	mu        sync.Mutex
	err       error
	seq       int
	workspace string
	lifetime  *organizationLifetime
	peak      uint64
	processes uint32
}

func startOrganizationClient(ctx context.Context, p OrganizationProfile) (*organizationClient, error) {
	if e := OrganizationCapacity(p); e != nil {
		return nil, e
	}
	workspace, e := os.MkdirTemp("", "ownward-organization-")
	if e != nil {
		return nil, e
	}
	child, cancel := context.WithCancel(ctx)
	c := &organizationClient{workspace: workspace, cancel: cancel, messages: make(chan organizationMessage, 16), done: make(chan struct{})}
	cmd := exec.CommandContext(child, p.Executable, "app-server", "--stdio", "-c", "features.codex_hooks=false", "-c", "project_doc_max_bytes=0", "-c", "features.shell_tool=false", "-c", "features.unified_exec=false", "-c", "features.plugins=false", "-c", "features.apps=false", "-c", "features.skip_host_skill_discovery=true", "-c", "web_search=\"disabled\"", "-c", "sqlite_home="+strconv.Quote(filepath.ToSlash(workspace)), "-c", "log_dir="+strconv.Quote(filepath.ToSlash(workspace)), "-c", "history.persistence=\"none\"")
	c.command = cmd
	cmd.Dir = workspace
	cmd.Env = organizationEnvironment(p.Home)
	cmd.Stderr = io.Discard // No material-bearing runtime log is copied into the adapter.
	c.input, e = cmd.StdinPipe()
	if e == nil {
		c.output, e = cmd.StdoutPipe()
	}
	if e != nil {
		cancel()
		os.Remove(workspace)
		return nil, e
	}
	configureOrganizationProcess(cmd)
	if e = cmd.Start(); e != nil {
		c.input.Close()
		c.output.Close()
		cancel()
		os.Remove(workspace)
		return nil, e
	}
	c.lifetime, e = attachOrganizationProcess(cmd, p.MemoryMiB)
	if e != nil {
		cmd.Process.Kill()
		cmd.Wait()
		cancel()
		os.Remove(workspace)
		return nil, e
	}
	go func() {
		defer close(c.done)
		defer close(c.messages)
		scanner := bufio.NewScanner(c.output)
		scanner.Buffer(make([]byte, 4096), organizationMessageBytes)
		for scanner.Scan() {
			var m organizationMessage
			if json.Unmarshal(scanner.Bytes(), &m) != nil {
				c.mu.Lock()
				c.err = errors.New("组织宿主协议无效")
				c.mu.Unlock()
				return
			}
			select {
			case c.messages <- m:
			case <-child.Done():
				return
			}
		}
		c.mu.Lock()
		c.err = scanner.Err()
		c.mu.Unlock()
	}()
	var init any
	startup, stop := context.WithTimeout(child, 15*time.Second)
	defer stop()
	if e = c.call(startup, "initialize", map[string]any{"clientInfo": map[string]string{"name": "ownward_organization", "version": "1"}, "capabilities": map[string]any{"experimentalApi": true}}, &init); e == nil {
		e = c.send(map[string]any{"method": "initialized"})
	}
	if e == nil {
		var config struct {
			Config struct {
				MCP              map[string]any `json:"mcp_servers"`
				Plugins          map[string]any `json:"plugins"`
				Notify           []string       `json:"notify"`
				InstructionsFile string         `json:"model_instructions_file"`
			}
		}
		e = c.call(startup, "config/read", map[string]any{"includeLayers": false}, &config)
		if e == nil && (len(config.Config.MCP) > 0 || len(config.Config.Plugins) > 0 || len(config.Config.Notify) > 0 || config.Config.InstructionsFile != "") {
			e = errors.New("组织专用宿主不能继承其他MCP、插件、命令通知或外部模型指令文件")
		}
	}
	if e != nil {
		c.close()
		return nil, e
	}
	return c, nil
}

func organizationEnvironment(home string) []string {
	env := make([]string, 0)
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(strings.ToUpper(key), "CODEX_") || strings.HasPrefix(strings.ToUpper(key), "OWNWARD_") || strings.EqualFold(key, "RUST_LOG") {
			continue
		}
		env = append(env, v)
	}
	return append(env, "CODEX_HOME="+home, "RUST_LOG=off")
}

func (c *organizationClient) send(v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > organizationMessageBytes {
		return errors.New("组织消息超过边界")
	}
	_, e = c.input.Write(append(b, '\n'))
	return e
}
func (c *organizationClient) next(ctx context.Context) (organizationMessage, error) {
	select {
	case <-ctx.Done():
		return organizationMessage{}, ctx.Err()
	case m, ok := <-c.messages:
		if !ok {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.err != nil {
				return m, c.err
			}
			return m, io.EOF
		}
		return m, nil
	}
}
func (c *organizationClient) call(ctx context.Context, method string, params, out any) error {
	c.seq++
	id := c.seq
	if e := c.send(map[string]any{"id": id, "method": method, "params": params}); e != nil {
		return e
	}
	for {
		m, e := c.next(ctx)
		if e != nil {
			return e
		}
		if m.Method != "" && len(m.ID) > 0 {
			return errors.New("组织宿主初始化要求额外权限")
		}
		if string(m.ID) != strconv.Itoa(id) {
			continue
		}
		if len(m.Error) > 0 {
			return errors.New("组织宿主拒绝协议请求: " + method)
		}
		return json.Unmarshal(m.Result, out)
	}
}
func (c *organizationClient) close() {
	if c.command == nil {
		return
	}
	c.peak, c.processes = c.lifetime.resources()
	c.cancel()
	c.input.Close()
	c.lifetime.close()
	c.output.Close()
	c.command.Wait()
	<-c.done
	c.command = nil
	// Only the private task directory allocated above is eligible for removal.
	if parent, e := filepath.Abs(os.TempDir()); e == nil && filepath.Dir(c.workspace) == parent && strings.HasPrefix(filepath.Base(c.workspace), "ownward-organization-") {
		os.RemoveAll(c.workspace)
	}
}
func (c *organizationClient) resources() (uint64, uint32) { return c.peak, c.processes }
