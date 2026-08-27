package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/kernelcatalog"
)

func TestSelectedGoContentFreezesOnlyTheNamedLegacyRule(t *testing.T) {
	source := []byte("package sample\nconst Unrelated = `noise`\nconst CollaborationRules = `stable rules`\nfunc runtime() {}\n")
	actual, err := selectedGoContent(source, "go-const=CollaborationRules")
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != "`stable rules`" {
		t.Fatalf("legacy product-rule identity included unrelated source: %q", actual)
	}
	if _, err := selectedGoContent(source, "go-const=Missing"); err == nil {
		t.Fatal("missing legacy rule was accepted")
	}
}

func TestVerifyRejectsAndDoesNotRepairOnDiskContractDrift(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	catalogPath := filepath.Join(root, "manifests", "kernel-generations", "v1", "catalog.json")
	catalog, err := kernelcatalog.Load(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Generations[0].Kernel.Contracts[0].DefinitionSHA256 = strings.Repeat("a", 64)
	encoded, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "--repository", root, "--catalog", path}); err == nil {
		t.Fatal("verify repaired and accepted a drifted catalog")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(encoded) {
		t.Fatal("verify mutated the sealed catalog input")
	}
}
