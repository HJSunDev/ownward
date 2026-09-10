package localowner

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVaultSeparatesConnectionsAndReplacesCredentials(t *testing.T) {
	v := Vault{Root: t.TempDir()}
	if err := v.Save("system-a", "owner", "test-owner-secret"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save("system-a", "agent", "test-agent-secret"); err != nil {
		t.Fatal(err)
	}
	if err := v.Save("system-a", "owner", "replacement-secret"); err != nil {
		t.Fatal(err)
	}
	value, err := v.Load("system-a", "owner")
	if err != nil || value != "replacement-secret" {
		t.Fatal("credential replacement failed", err)
	}
	if _, err := v.Load("system-b", "owner"); !os.IsNotExist(err) {
		t.Fatal("system identities not separated")
	}
	entries, _ := os.ReadDir(v.Root)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(v.Root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" && (bytes.Contains(data, []byte("replacement-secret")) || bytes.Contains(data, []byte("test-agent-secret"))) {
			t.Fatal("Windows credentials not OS protected")
		}
	}
}
