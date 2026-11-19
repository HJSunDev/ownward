package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
)

func TestSealAndVerifyRoundTripAndRejectDrift(t *testing.T) {
	repository := t.TempDir()
	for _, definition := range contract.Definitions() {
		full := filepath.Join(repository, filepath.FromSlash(definition.Source))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(definition.ID+"/v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	components := make([]composition.Component, 0, 7)
	for _, role := range []string{"authority-substrate", "semantic", "vector", "product-rules", "kernel", "access", "assembly"} {
		rel := filepath.ToSlash(filepath.Join("components", role+".go"))
		full := filepath.Join(repository, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package "+strings.ReplaceAll(role, "-", "")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		components = append(components, composition.Component{
			Role:    role,
			Content: []composition.Content{{Name: role, Path: rel}},
		})
	}
	manifestPath := filepath.Join(repository, "manifest.json")
	f, err := os.Create(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := composition.WriteJSON(f, composition.Manifest{
		Schema: composition.ManifestSchema, Name: "test-collaborative", Components: components,
	}); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()

	if err := run([]string{"seal", "--repository", repository, "--manifest", manifestPath, "--output", manifestPath}); err != nil {
		t.Fatalf("seal failed: %v", err)
	}
	if err := run([]string{"verify", "--repository", repository, "--manifest", manifestPath}); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repository, "components", "kernel.go"), []byte("package tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "--repository", repository, "--manifest", manifestPath}); err == nil {
		t.Fatal("verify accepted tampered component source")
	}
}
