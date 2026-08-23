package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	sharedMCPDescriptorEnvironment = "OWNWARD_SHARED_MCP_DESCRIPTOR"
	sharedMCPTokenEnvironment      = "OWNWARD_SHARED_MCP_TOKEN"
	sharedMCPIdentityEnvironment   = "OWNWARD_SHARED_MCP_IDENTITY"
	sharedMCPStatusPath            = "/__ownward/service"
	sharedMCPShutdownPath          = "/__ownward/shutdown"
)

type sharedMCPDescriptor struct {
	Schema          string `json:"schema"`
	PID             int    `json:"pid"`
	Endpoint        string `json:"endpoint"`
	BearerToken     string `json:"bearer_token"`
	ServiceIdentity string `json:"service_identity"`
	DataIdentity    string `json:"data_identity"`
	StartedAt       string `json:"started_at"`
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+transport.token)
	return transport.base.RoundTrip(clone)
}

func runSharedMCPConnector(ctx context.Context, dataDir, binaryVersion string, stdout, stderr io.Writer) error {
	descriptor, err := ensureSharedMCPService(ctx, dataDir, binaryVersion, stderr)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Transport: bearerTransport{token: descriptor.BearerToken, base: http.DefaultTransport}, Timeout: 2 * time.Minute}
	client := mcp.NewClient(&mcp.Implementation{Name: "ownward-connect-or-start", Version: binaryVersion}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: descriptor.Endpoint, HTTPClient: httpClient, MaxRetries: 1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return fmt.Errorf("连接共享 Ownward 内核失败: %w", err)
	}
	defer session.Close()
	initialize := session.InitializeResult()
	instructions := ""
	if initialize != nil {
		instructions = initialize.Instructions
	}
	proxy := mcp.NewServer(&mcp.Implementation{Name: "ownward", Version: binaryVersion}, &mcp.ServerOptions{Instructions: instructions, Capabilities: &mcp.ServerCapabilities{}})
	for tool, toolErr := range session.Tools(ctx, nil) {
		if toolErr != nil {
			return fmt.Errorf("读取共享 Ownward 工具契约失败: %w", toolErr)
		}
		copyOfTool := *tool
		proxy.AddTool(&copyOfTool, func(callContext context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return session.CallTool(callContext, &mcp.CallToolParams{Name: request.Params.Name, Arguments: request.Params.Arguments})
		})
	}
	if err := proxy.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("共享 Ownward stdio 连接器结束: %w", err)
	}
	return nil
}

func ensureSharedMCPService(ctx context.Context, dataDir, binaryVersion string, stderr io.Writer) (*sharedMCPDescriptor, error) {
	normalizedDataDir, err := normalizeDataDirectory(dataDir)
	if err != nil {
		return nil, err
	}
	runtimeDir := filepath.Join(normalizedDataDir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return nil, err
	}
	identity, dataIdentity, err := sharedMCPIdentity(normalizedDataDir, binaryVersion)
	if err != nil {
		return nil, err
	}
	lock, err := acquireServiceStartupLock(filepath.Join(runtimeDir, "mcp-service.lock"), 130*time.Second)
	if err != nil {
		return nil, err
	}
	defer lock.release()
	descriptorPath := filepath.Join(runtimeDir, "mcp-service.json")
	if existing, readErr := readSharedMCPDescriptor(descriptorPath); readErr == nil {
		aliveIdentity, probeErr := probeSharedMCP(ctx, existing)
		if probeErr == nil && existing.ServiceIdentity == identity && aliveIdentity == identity && existing.DataIdentity == dataIdentity {
			return existing, nil
		}
		if probeErr == nil {
			if shutdownErr := shutdownSharedMCP(ctx, existing); shutdownErr != nil {
				return nil, fmt.Errorf("已有 Ownward 内核身份不兼容且无法安全切换: %w", shutdownErr)
			}
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				if _, probeErr = probeSharedMCP(ctx, existing); probeErr != nil {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if probeErr == nil {
				return nil, errors.New("已有 Ownward 内核未在安全关闭期限内退出")
			}
		}
		_ = os.Remove(descriptorPath)
	}
	if err := startSharedMCPService(normalizedDataDir, descriptorPath, identity, stderr); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(120 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		descriptor, readErr := readSharedMCPDescriptor(descriptorPath)
		if readErr == nil && descriptor.ServiceIdentity == identity && descriptor.DataIdentity == dataIdentity {
			aliveIdentity, probeErr := probeSharedMCP(ctx, descriptor)
			if probeErr == nil && aliveIdentity == identity {
				return descriptor, nil
			}
			lastErr = probeErr
		} else if readErr != nil {
			lastErr = readErr
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("共享 Ownward 内核未在 120 秒内就绪: %w", lastErr)
}

func normalizeDataDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}
	if runtime.GOOS == "windows" {
		absolute = strings.ToLower(absolute)
	}
	return absolute, nil
}

func sharedMCPIdentity(dataDir, binaryVersion string) (string, string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return "", "", err
	}
	bundleManifest, err := os.ReadFile(filepath.Join(filepath.Dir(executable), "embedding", "manifest.json"))
	if err != nil {
		return "", "", fmt.Errorf("识别共享向量能力清单: %w", err)
	}
	dataHash := sha256.Sum256([]byte(dataDir))
	dataIdentity := "sha256:" + hex.EncodeToString(dataHash[:])
	hash := sha256.New()
	_, _ = hash.Write([]byte(binaryVersion))
	_, _ = hash.Write(binary)
	_, _ = hash.Write([]byte(dataIdentity))
	_, _ = hash.Write(bundleManifest)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), dataIdentity, nil
}

func startSharedMCPService(dataDir, descriptorPath, identity string, stderr io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	token := hex.EncodeToString(tokenBytes)
	logPath := filepath.Join(filepath.Dir(descriptorPath), "mcp-service.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	command := exec.Command(executable, "mcp-http", "--data-dir", dataDir, "--listen", "127.0.0.1:0")
	command.Env = append(os.Environ(), sharedMCPDescriptorEnvironment+"="+descriptorPath, sharedMCPTokenEnvironment+"="+token, sharedMCPIdentityEnvironment+"="+identity)
	command.Stdout = logFile
	command.Stderr = logFile
	configureSharedServiceProcess(command)
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("启动共享 Ownward 内核: %w", err)
	}
	if stderr != nil {
		_, _ = fmt.Fprintf(stderr, "Ownward shared core starting (pid %d)\n", command.Process.Pid)
	}
	_ = command.Process.Release()
	return logFile.Close()
}

func readSharedMCPDescriptor(path string) (*sharedMCPDescriptor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var descriptor sharedMCPDescriptor
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return nil, err
	}
	if descriptor.Schema != "ownward.shared-mcp/v1" || descriptor.PID <= 0 || descriptor.BearerToken == "" || descriptor.ServiceIdentity == "" || descriptor.DataIdentity == "" {
		return nil, errors.New("共享 Ownward 端点描述无效")
	}
	parsed, err := url.Parse(descriptor.Endpoint)
	if err != nil || parsed.Scheme != "http" {
		return nil, errors.New("共享 Ownward 端点不是有效的本机 HTTP 地址")
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("共享 Ownward 端点必须位于本机回环地址")
	}
	return &descriptor, nil
}

func probeSharedMCP(parent context.Context, descriptor *sharedMCPDescriptor) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, descriptor.Endpoint+sharedMCPStatusPath, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+descriptor.BearerToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("共享 Ownward 状态探测返回 %s", response.Status)
	}
	var status map[string]string
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return "", err
	}
	return status["service_identity"], nil
}

func shutdownSharedMCP(parent context.Context, descriptor *sharedMCPDescriptor) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, descriptor.Endpoint+sharedMCPShutdownPath, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+descriptor.BearerToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("共享 Ownward 安全关闭返回 %s", response.Status)
	}
	return nil
}

func publishSharedMCPDescriptorFromEnvironment(endpoint string) (func(), error) {
	path := strings.TrimSpace(os.Getenv(sharedMCPDescriptorEnvironment))
	if path == "" {
		return func() {}, nil
	}
	identity := strings.TrimSpace(os.Getenv(sharedMCPIdentityEnvironment))
	token := strings.TrimSpace(os.Getenv(sharedMCPTokenEnvironment))
	dataDir, err := normalizeDataDirectory(filepath.Dir(filepath.Dir(path)))
	if err != nil {
		return nil, err
	}
	dataHash := sha256.Sum256([]byte(dataDir))
	descriptor := &sharedMCPDescriptor{Schema: "ownward.shared-mcp/v1", PID: os.Getpid(), Endpoint: endpoint, BearerToken: token, ServiceIdentity: identity, DataIdentity: "sha256:" + hex.EncodeToString(dataHash[:]), StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if identity == "" || token == "" {
		return nil, errors.New("共享 Ownward 服务缺少启动身份或令牌")
	}
	if err := atomicWriteSharedMCPDescriptor(path, descriptor); err != nil {
		return nil, err
	}
	cleanup := func() {
		current, err := readSharedMCPDescriptor(path)
		if err == nil && current.PID == descriptor.PID && current.ServiceIdentity == descriptor.ServiceIdentity {
			_ = os.Remove(path)
		}
	}
	return cleanup, nil
}

func atomicWriteSharedMCPDescriptor(path string, descriptor *sharedMCPDescriptor) error {
	data, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mcp-service-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(temporaryPath, path)
}
