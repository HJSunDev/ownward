package main

import (
	"bytes"
	"context"
	"debug/pe"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/adapter/ownerwindow"
	"github.com/HJSunDev/ownward/internal/assembly"
	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/desktop"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDesktopFormalReleaseInterruptedPreparation(t *testing.T) {
	executable := os.Getenv("OWNWARD_ENTRY_RELEASE")
	if executable == "" {
		t.Skip("需要完整正式制品")
	}
	config := t.TempDir()
	t.Setenv("APPDATA", config)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("OWNWARD_DATA_DIR", "")
	if runtime.GOOS == "darwin" {
		t.Setenv("HOME", config)
	}
	statePath, err := desktop.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	p := desktop.Paths{Root: filepath.Join(t.TempDir(), "program"), State: statePath, Programs: filepath.Join(t.TempDir(), "menu")}
	data := filepath.Join(t.TempDir(), "information")
	budget, err := resourcebudget.New(16*resourcebudget.MiB, 4*resourcebudget.MiB)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("simulated interruption before initialization")
	_, err = boundedstore.OpenDeployment(context.Background(), data, boundedstore.DeploymentOptions{Options: boundedstore.Options{Budget: budget}, Initialize: func(context.Context, *boundedstore.Store, string) error { return interrupted }})
	if !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	root, err := desktop.ReleaseRoot(executable)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := assembly.PreflightSharedConnector(assembly.Collaborative, filepath.Join(root, "bin", "embedding"))
	if err != nil {
		t.Fatal(err)
	}
	intent, _ := json.Marshal(initializationIntent{Schema: "ownward.initialize/v1", Data: data, Composition: verified.Composition})
	if err = desktop.AtomicWrite(statePath+".preparing", intent, 0600); err != nil {
		t.Fatal(err)
	}
	var diagnostic bytes.Buffer
	if err = installAt(context.Background(), []string{"--data-dir", data}, io.Discard, &diagnostic, executable, p); err != nil {
		t.Fatal(err, diagnostic.String())
	}
	state, err := desktop.Load(statePath)
	if err != nil || state.Binding == nil || !sameDataDirectory(state.Binding.Data, data) {
		t.Fatal(state, err)
	}
	if _, err = os.Stat(statePath + ".preparing"); !os.IsNotExist(err) {
		t.Fatal("completed preparation still pending", err)
	}
}

func TestDesktopFormalRelease(t *testing.T) {
	executable := os.Getenv("OWNWARD_ENTRY_RELEASE")
	if executable == "" {
		t.Skip("需要完整正式制品；只在隔离目录创建系统启动文件")
	}
	config := t.TempDir()
	t.Setenv("APPDATA", config)
	t.Setenv("XDG_CONFIG_HOME", config)
	if runtime.GOOS == "darwin" {
		t.Setenv("HOME", config)
	}
	t.Setenv("OWNWARD_DATA_DIR", "")
	statePath, err := desktop.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	p := desktop.Paths{Root: filepath.Join(t.TempDir(), "program"), State: statePath, Programs: filepath.Join(t.TempDir(), "menu"), Desktop: filepath.Join(t.TempDir(), "desktop")}
	data := filepath.Join(t.TempDir(), "information")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var out, diagnostic bytes.Buffer
	start := time.Now()
	if err = installAt(ctx, []string{"--new", "--desktop", "--data-dir", data}, &out, &diagnostic, executable, p); err != nil {
		t.Fatalf("install: %v (%s)", err, diagnostic.String())
	}
	t.Logf("verified installation=%s", time.Since(start))
	installed, err := desktop.Load(p.State)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Binding == nil || !sameDataDirectory(installed.Binding.Data, data) {
		t.Fatal("wrong selection")
	}
	if runtime.GOOS == "windows" {
		binary, err := pe.Open(installed.Executable)
		if err != nil {
			t.Fatal(err)
		}
		header, ok := binary.OptionalHeader.(*pe.OptionalHeader64)
		if !ok || header.Subsystem != 2 {
			t.Fatal("clickable release would flash a console")
		}
		binary.Close()
	}
	before, err := assembly.ReadControlAt(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = installAt(ctx, []string{"--data-dir", data}, io.Discard, &diagnostic, executable, p); err != nil {
		t.Fatal("retry", err)
	}
	after, err := assembly.ReadControlAt(data)
	if err != nil || before.InformationControl.SystemID != after.InformationControl.SystemID {
		t.Fatal("retry initialized another authority", err)
	}
	for round := 0; round < 2; round++ {
		start = time.Now()
		client := mcp.NewClient(&mcp.Implementation{Name: "entry-verification", Version: "1"}, nil)
		cmd := exec.Command(installed.Executable, "mcp")
		diagnostic.Reset()
		cmd.Stderr = &diagnostic
		configureSharedServiceProcess(cmd)
		session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
		if err != nil {
			t.Fatalf("connect: %v (%s)", err, diagnostic.String())
		}
		descriptor, err := readSharedMCPDescriptor(filepath.Join(data, "runtime", "mcp-service.json"))
		if err != nil {
			t.Fatal(err)
		}
		stop := func() { session.Close(); _ = shutdownSharedMCP(context.Background(), descriptor) }
		vault, err := localowner.ForData(data)
		if err != nil {
			stop()
			t.Fatal(err)
		}
		credential, err := vault.Load(installed.Binding.System, "owner")
		if err != nil {
			stop()
			t.Fatal(err)
		}
		host := hostConnector{descriptor: descriptor}
		var entry struct {
			Entry string `json:"entry"`
		}
		if err = host.controlCall(ctx, "owner-window", credential, struct{}{}, &entry); err != nil {
			stop()
			t.Fatal(err)
		}
		address, err := url.Parse(entry.Entry)
		if err != nil {
			stop()
			t.Fatal("invalid entry URL")
		}
		var identity struct {
			Token string `json:"session"`
		}
		if err = entryHTTP(ctx, descriptor.Endpoint, "bootstrap", "", map[string]string{"token": address.Fragment}, &identity); err != nil {
			stop()
			t.Fatal(err)
		}
		if identity.Token == "" {
			stop()
			t.Fatal("no browser session")
		}
		var page contract.OwnerPage
		if err = entryHTTP(ctx, descriptor.Endpoint, "query", identity.Token, contract.OwnerQuery{View: "drafts"}, &page); err != nil {
			stop()
			t.Fatal(err)
		}
		if round == 0 {
			var result contract.OwnerResult
			if err = entryHTTP(ctx, descriptor.Endpoint, "action", identity.Token, contract.OwnerAction{Action: "create_draft", Text: textPtr("entry restart keeps this draft")}, &result); err != nil {
				stop()
				t.Fatal(err)
			}
		} else if len(page.Drafts) != 1 {
			stop()
			t.Fatal("independent reopen lost the saved draft")
		}
		t.Logf("round %d: verified browser API ready=%s", round+1, time.Since(start))
		// 重复打开必须复用仍持有写锁的服务，不能将活动资料误报为不可用。
		repeat := mcp.NewClient(&mcp.Implementation{Name: "repeat-open", Version: "1"}, nil)
		repeatCommand := exec.Command(installed.Executable, "mcp")
		var repeatDiagnostic bytes.Buffer
		repeatCommand.Stderr = &repeatDiagnostic
		configureSharedServiceProcess(repeatCommand)
		repeated, err := repeat.Connect(ctx, &mcp.CommandTransport{Command: repeatCommand}, nil)
		if err != nil {
			stop()
			t.Fatal("repeat open", err, repeatDiagnostic.String())
		}
		repeated.Close()
		reused, err := readSharedMCPDescriptor(filepath.Join(data, "runtime", "mcp-service.json"))
		if err != nil || reused.PID != descriptor.PID {
			stop()
			t.Fatal("replaced live authority", err)
		}
		if round == 1 && os.Getenv("OWNWARD_ENTRY_BROWSER_VERIFY") != "" {
			if err = host.controlCall(ctx, "owner-window", credential, struct{}{}, &entry); err != nil {
				stop()
				t.Fatal(err)
			}
			verifyDir := os.Getenv("OWNWARD_ENTRY_BROWSER_VERIFY")
			if err = os.Remove(filepath.Join(verifyDir, "browser.done")); err != nil && !os.IsNotExist(err) {
				stop()
				t.Fatal(err)
			}
			redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, entry.Entry, http.StatusFound) }))
			defer redirect.Close()
			encoded, _ := json.Marshal(map[string]string{"url": redirect.URL, "origin": descriptor.Endpoint})
			if err = os.WriteFile(filepath.Join(verifyDir, "browser.json"), encoded, 0600); err != nil {
				stop()
				t.Fatal(err)
			}
			deadline := time.Now().Add(100 * time.Second)
			confirmed := false
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(verifyDir, "browser.done")); err == nil {
					confirmed = true
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !confirmed {
				stop()
				t.Fatal("浏览器检查未在验收窗口内完成")
			}
		}
		stop()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := probeSharedMCP(ctx, descriptor); err != nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// An unavailable selection cannot be silently recreated by installation.
	installed, err = desktop.Load(p.State)
	if err != nil {
		t.Fatal(err)
	}
	installed.Binding.Data = filepath.Join(t.TempDir(), "missing")
	if err = desktop.Save(p.State, installed); err != nil {
		t.Fatal(err)
	}
	if err = installAt(ctx, []string{"--new"}, io.Discard, io.Discard, executable, p); err == nil {
		t.Fatal("missing bound data replaced with an empty store")
	}
	if _, err = os.Stat(installed.Binding.Data); !os.IsNotExist(err) {
		t.Fatal("created a substitute store", err)
	}
}

func entryHTTP(ctx context.Context, origin, path, token string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", origin+ownerwindow.Prefix+"v1/"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("X-Ownward-View", contract.OwnerViewSchema)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &entryHTTPError{resp.StatusCode}
	}
	return json.NewDecoder(resp.Body).Decode(output)
}

type entryHTTPError struct{ code int }

func (e *entryHTTPError) Error() string { return http.StatusText(e.code) }

func TestDesktopFormalReleaseReusesCompatibleManagedService(t *testing.T) {
	executable := os.Getenv("OWNWARD_ENTRY_RELEASE")
	if executable == "" {
		t.Skip("requires complete formal release resources")
	}
	if os.Getenv("OWNWARD_TEST_MANAGED_ENTRY_CHILD") != "1" {
		// The production identity reads resources beside its executable. Run the
		// test binary in an isolated release-shaped directory, not the Go cache.
		root := t.TempDir()
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		child := filepath.Join(root, filepath.Base(self))
		for target, source := range map[string]string{child: self, filepath.Join(root, "embedding", "manifest.json"): filepath.Join(filepath.Dir(executable), "embedding", "manifest.json")} {
			data, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(target, data, 0700); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, child, "-test.run=^"+t.Name()+"$", "-test.v")
		cmd.Env = append(os.Environ(), "OWNWARD_TEST_MANAGED_ENTRY_CHILD=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("managed entry check: %v\n%s", err, out)
		}
		t.Log(string(out))
		return
	}
	root := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	s := installation{Root: filepath.Join(root, "service"), DataDir: filepath.Join(root, "data")}
	bundle := filepath.Join(filepath.Dir(executable), "embedding")
	r, err := assembly.Open(assembly.Request{DataDir: s.DataDir, ProductSemantics: assembly.Collaborative, VectorBundleDir: bundle})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err = r.UserControl().InitializeOwner("test owner"); err != nil {
		t.Fatal(err)
	}
	if err = s.vault().Save(ownerRecoveryScope(s.DataDir), "owner-recovery", "synthetic-test-recovery"); err != nil {
		t.Fatal(err)
	}
	stop, err := startManagedLocal(s, r)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d, err := ensureSharedMCPService(ctx, s.DataDir, "different-launcher-version", r.Composition().Composition, io.Discard)
	if err != nil || d.ManagedRoot != s.Root {
		t.Fatal("compatible live service was not reused", err)
	}
	if _, err = ensureSharedMCPService(ctx, s.DataDir, "different-launcher-version", "incompatible-composition", io.Discard); err == nil {
		t.Fatal("incompatible live service accepted")
	}
	if _, err = probeSharedMCP(ctx, d); err != nil {
		t.Fatal("rejected open interrupted live service", err)
	}
	// A connected application can keep an SSE stream open. Stopping Ownward
	// must cancel that request, rather than waiting for the drain timeout.
	client := &http.Client{Timeout: 10 * time.Second}
	init, _ := http.NewRequest("POST", d.Endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"stop-verification","version":"1"}}}`))
	init.Header.Set("Authorization", "Bearer "+d.BearerToken)
	init.Header.Set("Content-Type", "application/json")
	init.Header.Set("Accept", "application/json, text/event-stream")
	response, err := client.Do(init)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	session := response.Header.Get("Mcp-Session-Id")
	if response.StatusCode != 200 || session == "" {
		t.Fatal("could not initialize connected application", response.StatusCode)
	}
	stream, _ := http.NewRequest("GET", d.Endpoint, nil)
	stream.Header.Set("Authorization", "Bearer "+d.BearerToken)
	stream.Header.Set("Accept", "text/event-stream")
	stream.Header.Set("Mcp-Session-Id", session)
	stream.Header.Set("Mcp-Protocol-Version", "2025-03-26")
	response, err = client.Do(stream)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("connected application has no active stream", response.StatusCode)
	}
	started := time.Now()
	stop()
	if time.Since(started) > 2*time.Second {
		t.Fatal("stopping waited for the active connection instead of cancelling it")
	}
	if _, err = io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal("active stream was not closed cleanly", err)
	}
}

// 仅在人工观察真实原生窗口时启用；程序、菜单、账户记录和资料均隔离。
func TestDesktopNativeWorkflow(t *testing.T) {
	verifyDir := os.Getenv("OWNWARD_ENTRY_DESKTOP_VERIFY")
	executable := os.Getenv("OWNWARD_ENTRY_RELEASE")
	if runtime.GOOS != "windows" || verifyDir == "" || executable == "" {
		t.Skip("需要 Windows 原生窗口验收")
	}
	root := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(root, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "local"))
	t.Setenv("OWNWARD_DATA_DIR", "")
	statePath, err := desktop.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	defer func() {
		for _, data := range []string{filepath.Join(root, "config", "Ownward"), filepath.Join(root, "information")} {
			if d, e := readSharedMCPDescriptor(filepath.Join(data, "runtime", "mcp-service.json")); e == nil {
				_ = shutdownSharedMCP(context.Background(), d)
			}
		}
	}()
	// 使用显式临时启动目录；Windows KnownFolder 不受 APPDATA 隔离。
	// 首开选择窗口另由 desktop 包的原生窗口验收覆盖，避免人工误选写真实菜单。
	p := desktop.Paths{Root: filepath.Join(root, "program"), State: statePath, Programs: filepath.Join(root, "menu"), Desktop: filepath.Join(root, "desktop")}
	data := filepath.Join(root, "information")
	var diagnostic bytes.Buffer
	if err = installAt(ctx, []string{"--new", "--data-dir", data, "--desktop"}, io.Discard, &diagnostic, executable, p); err != nil {
		t.Fatal(err, diagnostic.String())
	}
	installed, err := desktop.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	// 与系统启动文件相同的公开命令；故意继承错误环境值验证不会改选资料。
	t.Setenv("OWNWARD_DATA_DIR", filepath.Join(root, "wrong-selection"))
	start := time.Now()
	if err = runInstalled(ctx, installed.Executable, []string{"owner-window", "--interactive"}, io.Discard, &diagnostic); err != nil {
		t.Fatal(err, diagnostic.String())
	}
	t.Logf("native entry through browser dispatch=%s", time.Since(start))
	if _, err = os.Stat(filepath.Join(root, "wrong-selection")); !os.IsNotExist(err) {
		t.Fatal("inherited environment redirected entry", err)
	}
	d, err := readSharedMCPDescriptor(filepath.Join(data, "runtime", "mcp-service.json"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, d.Endpoint+ownerwindow.Prefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	page, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(page, []byte("<title>Ownward</title>")) {
		t.Fatalf("entry page unavailable: status=%d error=%v", response.StatusCode, err)
	}
	b, _ := json.Marshal(map[string]string{"origin": d.Endpoint, "phase": "browser_dispatched_page_served"})
	if err = os.WriteFile(filepath.Join(verifyDir, "native.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	// 页面交互和视觉结论由浏览器验收记录单列，不把 ShellExecute 成功当作 UI 验收。
	t.Log("native entry dispatched default browser and served the owner page")
}
