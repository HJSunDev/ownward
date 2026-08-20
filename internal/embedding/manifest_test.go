package embedding

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBundleBindsArtifactsAndCompleteSpaceDefinition(t *testing.T) {
	root := t.TempDir()
	model := []byte("model")
	runtime := []byte("runtime")
	if err := os.MkdirAll(filepath.Join(root, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "model"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "model", "embedding.gguf"), model, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runtime", "llama-server.exe"), runtime, 0o700); err != nil {
		t.Fatal(err)
	}
	legal := map[string][]byte{
		"legal/embeddinggemma/GEMMA_TERMS_OF_USE.md":          []byte("terms"),
		"legal/embeddinggemma/GEMMA_PROHIBITED_USE_POLICY.md": []byte("policy"),
		"legal/embeddinggemma/USE_RESTRICTIONS.md":            []byte("restrictions"),
		"legal/embeddinggemma/MODIFICATIONS.md":               []byte("modifications"),
		"legal/embeddinggemma/NOTICE":                         []byte("notice"),
		"legal/llama.cpp/LICENSE":                             []byte("license"),
	}
	legalDigests := make(map[string]string, len(legal))
	for path, content := range legal {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(path))), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), content, 0o600); err != nil {
			t.Fatal(err)
		}
		legalDigests[path] = digest(content)
	}
	manifest := Manifest{
		Schema: ManifestSchema, Capability: "embeddinggemma-300m-q8/llama.cpp-b10488",
		Model: ModelArtifact{Path: "model/embedding.gguf", SHA256: digest(model)},
		Runtime: RuntimeArtifact{
			Entry: "runtime/llama-server.exe", SourceArchiveSHA256: digest([]byte("archive")),
			Files: map[string]string{"runtime/llama-server.exe": digest(runtime)},
		},
		Legal: LegalArtifacts{Files: legalDigests},
		Space: SpaceDefinition{
			Dimensions: 512, SourceDimensions: 768,
			QueryPrefix: "task: search result | query: ", DocumentPrefix: "title: none | text: ",
			Pooling: "mean", Normalization: "l2", Truncation: "prefix",
		},
	}
	acceptanceID, err := ComputeAcceptanceID(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Legal.AcceptanceID = acceptanceID
	spaceID, err := ComputeSpaceID(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Space.ID = spaceID
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Space.ID != spaceID || bundle.ModelPath == "" || bundle.RuntimePath == "" {
		t.Fatalf("bundle identity was not preserved: %#v", bundle)
	}
	if !bundle.verified {
		t.Fatal("fully loaded bundle did not retain its integrity verification state")
	}
	inspected, err := InspectBundle(root)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.verified {
		t.Fatal("manifest-only inspection was marked as fully verified")
	}
	if _, err := OpenManagedBundle(inspected); err == nil {
		t.Fatal("runtime accepted a bundle without complete artifact verification")
	}
	if err := os.WriteFile(bundle.ModelPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBundle(root); err == nil {
		t.Fatal("tampered model was accepted")
	}
}

func digest(value []byte) string {
	result := sha256.Sum256(value)
	return hex.EncodeToString(result[:])
}
