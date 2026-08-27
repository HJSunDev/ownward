package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestIsolatedReleaseCLIAndMCPAssembleWithoutRepository(t *testing.T) {
	clearModelEnvironment(t)
	binary, root, sourceVector := buildIsolatedRelease(t)
	dataDir := filepath.Join(root, "data")
	rules := runPackagedCLI(t, binary, root, "rules", "--data-dir", dataDir)
	if !bytes.Contains(rules, []byte("Ownward")) {
		t.Fatalf("isolated release CLI did not assemble: %s", rules)
	}

	mcpData := filepath.Join(root, "mcp-data")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "isolated-release-test", Version: "1"}, nil)
	command := exec.Command(binary, "mcp", "--data-dir", mcpData)
	command.Dir = root
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("isolated release MCP failed: %v", err)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_rules", Arguments: map[string]any{}}); err != nil {
		t.Fatalf("isolated release MCP did not assemble: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	descriptor, err := readSharedMCPDescriptor(filepath.Join(mcpData, "runtime", "mcp-service.json"))
	if err != nil {
		t.Fatal(err)
	}

	manifestPath := filepath.Join(root, "bin", "embedding", "manifest.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var vectorManifest struct {
		Model struct {
			Path string `json:"path"`
		} `json:"model"`
	}
	if err := json.Unmarshal(manifestBytes, &vectorManifest); err != nil || vectorManifest.Model.Path == "" {
		t.Fatalf("read packaged model identity: %v", err)
	}
	modelPath := filepath.Join(root, "bin", "embedding", filepath.FromSlash(vectorManifest.Model.Path))
	if err := os.Remove(modelPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, []byte("tampered-model"), 0o644); err != nil {
		t.Fatal(err)
	}

	secondClient := mcp.NewClient(&mcp.Implementation{Name: "isolated-release-reuse-test", Version: "1"}, nil)
	command = exec.Command(binary, "mcp", "--data-dir", mcpData)
	command.Dir = root
	secondSession, err := secondClient.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
	if err != nil {
		t.Fatalf("repeated connector rehashed full model instead of reusing shared service: %v", err)
	}
	if _, err := secondSession.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_rules", Arguments: map[string]any{}}); err != nil {
		t.Fatalf("repeated connector did not reuse shared service: %v", err)
	}
	if err := secondSession.Close(); err != nil {
		t.Fatal(err)
	}
	reused, err := readSharedMCPDescriptor(filepath.Join(mcpData, "runtime", "mcp-service.json"))
	if err != nil || reused.PID != descriptor.PID || reused.ServiceIdentity != descriptor.ServiceIdentity {
		t.Fatalf("repeated connector did not retain the matching shared service: %v %#v", err, reused)
	}
	if err := shutdownSharedMCP(context.Background(), descriptor); err != nil {
		t.Fatal(err)
	}

	fullValidationData := filepath.Join(root, "full-validation-data")
	command = exec.Command(binary, "rules", "--data-dir", fullValidationData)
	command.Dir = root
	if output, err := command.CombinedOutput(); err == nil || !bytes.Contains(output, []byte("校验向量模型")) {
		t.Fatalf("runtime owner did not reject tampered full model: %v %s", err, output)
	}
	if _, err := os.Stat(fullValidationData); !os.IsNotExist(err) {
		t.Fatalf("full runtime verification failure created product state: %v", err)
	}
	if err := os.Remove(modelPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(sourceVector, filepath.FromSlash(vectorManifest.Model.Path)), modelPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(manifestBytes, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	tamperedData := filepath.Join(root, "tampered-data")
	command = exec.Command(binary, "rules", "--data-dir", tamperedData)
	command.Dir = root
	if output, err := command.CombinedOutput(); err == nil || !bytes.Contains(output, []byte("内置组合身份不一致")) {
		t.Fatalf("tampered packaged vector was not rejected: %v %s", err, output)
	}
	if _, err := os.Stat(tamperedData); !os.IsNotExist(err) {
		t.Fatalf("tampered package created product state: %v", err)
	}
	tamperedMCP := filepath.Join(root, "tampered-mcp")
	command = exec.Command(binary, "mcp", "--data-dir", tamperedMCP)
	command.Dir = root
	if output, err := command.CombinedOutput(); err == nil || !bytes.Contains(output, []byte("内置组合身份不一致")) {
		t.Fatalf("tampered packaged MCP was not rejected: %v %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(tamperedMCP, "runtime", "mcp-service.json")); !os.IsNotExist(err) {
		t.Fatalf("tampered package created a shared descriptor: %v", err)
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

func TestSharedMCPIdentityIncludesActiveComposition(t *testing.T) {
	first, firstData := sharedMCPIdentityFromArtifacts("C:/data", "v1", []byte("binary"), []byte("vector"), "composition-a")
	second, secondData := sharedMCPIdentityFromArtifacts("C:/data", "v1", []byte("binary"), []byte("vector"), "composition-b")
	if first == second || firstData != secondData {
		t.Fatalf("composition did not independently affect shared service identity: %q %q / %q %q", first, second, firstData, secondData)
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

func runPackagedCLI(t *testing.T, binary, directory string, args ...string) []byte {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run %v: %v\noutput: %s", args, err, output)
	}
	return output
}

func buildIsolatedRelease(t *testing.T) (string, string, string) {
	t.Helper()
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(repository, "manifests", "compositions", "v1", "current-collaborative.json")
	manifest, err := composition.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composition.Verify(repository, manifest); err != nil {
		t.Fatalf("source composition was not build-ready: %v", err)
	}
	vectorManifest := ""
	for _, component := range manifest.Components {
		if component.Role != "vector" {
			continue
		}
		for _, content := range component.Content {
			if content.Name == "manifest.json" {
				vectorManifest = filepath.Join(repository, filepath.FromSlash(content.Path))
			}
		}
	}
	if vectorManifest == "" {
		t.Fatal("source composition did not bind the packaged vector manifest")
	}
	if _, err := os.Stat(vectorManifest); os.IsNotExist(err) {
		t.Skip("frozen release vector artifacts are not available in this checkout")
	} else if err != nil {
		t.Fatal(err)
	}
	releaseRoot, err := os.MkdirTemp(filepath.Dir(repository), ".ownward-release-isolation-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			removeErr := os.RemoveAll(releaseRoot)
			if _, statErr := os.Stat(releaseRoot); os.IsNotExist(statErr) {
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("remove isolated release fixture: %v", removeErr)
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	})
	binDir := filepath.Join(releaseRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binDir, "ownward.exe")
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w -X main.version=isolated-release", "-o", binary, "./cmd/ownward")
	build.Dir = repository
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build isolated release: %v\n%s", err, output)
	}
	if err := linkReleaseTree(filepath.Dir(vectorManifest), filepath.Join(binDir, "embedding")); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{".git", "manifests", ".tmp"} {
		if _, err := os.Stat(filepath.Join(releaseRoot, forbidden)); !os.IsNotExist(err) {
			t.Fatalf("isolated release contains %s: %v", forbidden, err)
		}
	}
	return binary, releaseRoot, filepath.Dir(vectorManifest)
}

func linkReleaseTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if !entry.Type().IsRegular() {
			return errors.New("release fixture contains a non-regular vector artifact")
		}
		if filepath.ToSlash(relative) != "manifest.json" {
			if err := os.Link(path, destination); err == nil {
				return nil
			}
		}
		sourceFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer sourceFile.Close()
		targetFile, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(targetFile, sourceFile)
		closeErr := targetFile.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
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
