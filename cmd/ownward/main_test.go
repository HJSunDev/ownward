package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/HJSunDev/ownward/internal/core"
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
	if created.Information.ID == "" || created.Information.Revision != 1 || created.Organization.Status != "degraded" {
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
