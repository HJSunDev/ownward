package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/core"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCLICompletesAssetLifecycleAcrossIndependentInvocations(t *testing.T) {
	clearModelEnvironment(t)
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	createdOutput := runCLI(t, "create", "--data-dir", dataDir, "--content", "只运行覆盖当前变更的最小充分测试。")
	var created core.MutationResult
	if err := json.Unmarshal(createdOutput, &created); err != nil {
		t.Fatal(err)
	}
	if created.Information.ID == "" || created.Information.Revision != 1 || created.Organization.Status != "pending" {
		t.Fatalf("unexpected create result: %#v", created)
	}

	readOutput := runCLI(t, "read", "--data-dir", dataDir, "--id", created.Information.ID)
	if !bytes.Contains(readOutput, []byte(created.Information.ID)) {
		t.Fatalf("read did not return the stable identity: %s", readOutput)
	}
	updatedContent := "开发期间只运行最小充分测试，稳定后再执行完整验证。"
	updatedOutput := runCLI(t, "update", "--data-dir", dataDir, "--id", created.Information.ID, "--revision", "1", "--content", updatedContent)
	var updated core.MutationResult
	if err := json.Unmarshal(updatedOutput, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Information.Revision != 2 || updated.Information.Content != updatedContent {
		t.Fatalf("unexpected update result: %#v", updated)
	}

	searchOutput := runCLI(t, "search", "--data-dir", dataDir, "--query", "什么时候执行完整验证")
	var results []core.SearchResult
	if err := json.Unmarshal(searchOutput, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != created.Information.ID {
		t.Fatalf("unexpected search result: %#v", results)
	}

	backup := filepath.Join(root, "backup.ownward")
	runCLI(t, "backup", "--data-dir", dataDir, "--output", backup)
	restoredDir := filepath.Join(root, "restored")
	runCLI(t, "restore", "--data-dir", restoredDir, "--backup", backup)
	restoredOutput := runCLI(t, "read", "--data-dir", restoredDir, "--id", created.Information.ID)
	if !bytes.Contains(restoredOutput, []byte(updatedContent)) {
		t.Fatalf("restored CLI data differs: %s", restoredOutput)
	}
}

func TestBearerTokenHandlerRejectsMissingOrWrongCredentials(t *testing.T) {
	handler := bearerTokenHandler(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}), "secret")
	for _, authorization := range []string{"", "Bearer wrong", "Basic secret"} {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", nil)
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q returned %d", authorization, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid bearer token returned %d", response.Code)
	}
}

type fakeHTTPMCPServer struct{}

func (fakeHTTPMCPServer) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) })
}

func TestSharedMCPServicePublishesAuthenticatedIdentityAndShutsDown(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	descriptorPath := filepath.Join(dataDir, "runtime", "mcp-service.json")
	t.Setenv(sharedMCPDescriptorEnvironment, descriptorPath)
	t.Setenv(sharedMCPTokenEnvironment, "test-token")
	t.Setenv(sharedMCPIdentityEnvironment, "sha256:"+strings.Repeat("a", 64))
	result := make(chan error, 1)
	go func() {
		result <- runHTTPMCP(context.Background(), fakeHTTPMCPServer{}, "127.0.0.1:0", "test-token", &bytes.Buffer{})
	}()
	var descriptor *sharedMCPDescriptor
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		descriptor, err = readSharedMCPDescriptor(descriptorPath)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if descriptor == nil {
		t.Fatal("shared service did not publish its descriptor")
	}
	identity, err := probeSharedMCP(context.Background(), descriptor)
	if err != nil || identity != descriptor.ServiceIdentity {
		t.Fatalf("shared service identity probe failed: %v %q", err, identity)
	}
	wrong := *descriptor
	wrong.BearerToken = "wrong"
	if _, err := probeSharedMCP(context.Background(), &wrong); err == nil {
		t.Fatal("shared service accepted a wrong bearer token")
	}
	if err := shutdownSharedMCP(context.Background(), descriptor); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shared service did not shut down")
	}
	if _, err := os.Stat(descriptorPath); !os.IsNotExist(err) {
		t.Fatalf("shared descriptor survived owner shutdown: %v", err)
	}
}

func TestSharedMCPDescriptorRejectsNonLoopbackEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "descriptor.json")
	descriptor := &sharedMCPDescriptor{Schema: "ownward.shared-mcp/v1", PID: 1, Endpoint: "http://192.0.2.1:9999", BearerToken: "secret", ServiceIdentity: "identity", DataIdentity: "data"}
	if err := atomicWriteSharedMCPDescriptor(path, descriptor); err != nil {
		t.Fatal(err)
	}
	if _, err := readSharedMCPDescriptor(path); err == nil || !strings.Contains(err.Error(), "回环") {
		t.Fatalf("non-loopback descriptor was accepted: %v", err)
	}
}

func TestSharedMCPExternalBinaryConcurrentClients(t *testing.T) {
	binary := os.Getenv("OWNWARD_SHARED_MCP_BINARY")
	if binary == "" {
		t.Skip("set OWNWARD_SHARED_MCP_BINARY to run the packaged shared-core test")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	type connection struct {
		session *mcp.ClientSession
		err     error
	}
	ready := make(chan connection, 2)
	for index := 0; index < 2; index++ {
		go func(index int) {
			client := mcp.NewClient(&mcp.Implementation{Name: "shared-test", Version: "1"}, nil)
			command := exec.Command(binary, "mcp", "--data-dir", dataDir)
			session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
			ready <- connection{session: session, err: err}
		}(index)
	}
	connections := make([]*mcp.ClientSession, 0, 2)
	for range 2 {
		result := <-ready
		if result.err != nil {
			t.Fatalf("concurrent stdio connector failed: %v", result.err)
		}
		connections = append(connections, result.session)
	}
	defer func() {
		for _, session := range connections {
			_ = session.Close()
		}
		descriptor, err := readSharedMCPDescriptor(filepath.Join(dataDir, "runtime", "mcp-service.json"))
		if err == nil {
			_ = shutdownSharedMCP(context.Background(), descriptor)
		}
	}()
	created, err := connections[0].CallTool(ctx, &mcp.CallToolParams{Name: "ownward_create", Arguments: map[string]any{"content": "shared-core-integration-value"}})
	if err != nil || created.IsError {
		t.Fatalf("first client could not create through shared core: %v %#v", err, created)
	}
	encoded, _ := json.Marshal(created.StructuredContent)
	var createResult struct {
		Result struct {
			Information struct {
				ID string `json:"id"`
			} `json:"information"`
		} `json:"result"`
	}
	if err := json.Unmarshal(encoded, &createResult); err != nil || createResult.Result.Information.ID == "" {
		t.Fatalf("create result had no stable identity: %v %s", err, encoded)
	}
	read, err := connections[1].CallTool(ctx, &mcp.CallToolParams{Name: "ownward_read", Arguments: map[string]any{"id": createResult.Result.Information.ID}})
	if err != nil || read.IsError {
		t.Fatalf("second client could not read first client result: %v %#v", err, read)
	}
	readJSON, _ := json.Marshal(read.StructuredContent)
	if !bytes.Contains(readJSON, []byte("shared-core-integration-value")) {
		t.Fatalf("clients did not observe one authoritative state: %s", readJSON)
	}
	if err := connections[0].Close(); err != nil {
		t.Fatal(err)
	}
	connections = connections[1:]
	if _, err := connections[0].CallTool(ctx, &mcp.CallToolParams{Name: "ownward_rules", Arguments: map[string]any{}}); err != nil {
		t.Fatalf("remaining client lost the shared core when another client exited: %v", err)
	}
}

func runCLI(t *testing.T, args ...string) []byte {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run %v: %v\nstderr: %s", args, err, stderr.String())
	}
	return stdout.Bytes()
}

func clearModelEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"OWNWARD_MODEL_BASE_URL", "OWNWARD_MODEL_API_KEY", "OWNWARD_CHAT_MODEL",
		"OWNWARD_EMBEDDING_MODEL", "OWNWARD_EMBEDDING_DIMENSIONS",
	} {
		t.Setenv(name, "")
	}
}
