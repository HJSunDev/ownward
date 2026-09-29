package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/releasebundle"
)

func sampleRelease(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	m := releasebundle.Manifest{Schema: releasebundle.ManifestSchema, Candidate: "test-v1", OS: runtime.GOOS, Arch: runtime.GOARCH, Executable: "bin/" + releasebundle.ExecutableName(runtime.GOOS), Files: map[string]string{}}
	for _, name := range []string{m.Entry(), "bin/embedding/manifest.json", "README.md", "LICENSE", "bin/ownward.png", "bin/ownward.ico", "bin/ownward.icns"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		body := []byte("fixture " + name)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(body)
		m.Files[name] = hex.EncodeToString(sum[:])
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestInstallChecksBytesBeforePublishingAndReusesSameRelease(t *testing.T) {
	root, dest := sampleRelease(t), t.TempDir()
	r, exe, err := Install(context.Background(), root, dest)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(exe) {
		t.Fatal(exe)
	}
	r2, exe2, err := Install(context.Background(), root, dest)
	if err != nil || r.ID != r2.ID || exe != exe2 {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if _, _, err = Install(context.Background(), root, other); err == nil {
		t.Fatal("accepted corrupt release")
	}
	entries, _ := os.ReadDir(filepath.Join(other, "releases"))
	if len(entries) != 0 {
		t.Fatal("published partial install")
	}
	if err := os.WriteFile(exe, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = Install(context.Background(), root, dest); err == nil {
		t.Fatal("reused corrupt installation")
	}
}

func TestInspectRejectsPlatformAndEscapingManifest(t *testing.T) {
	for _, variant := range []string{"platform", "escape", "entry"} {
		t.Run(variant, func(t *testing.T) {
			root := sampleRelease(t)
			path := filepath.Join(root, "manifest.json")
			raw, _ := os.ReadFile(path)
			var m releasebundle.Manifest
			_ = json.Unmarshal(raw, &m)
			switch variant {
			case "platform":
				m.Arch = "not-this-cpu"
			case "escape":
				m.Files["../outside"] = strings.Repeat("0", 64)
			case "entry":
				m.Executable = "../other"
			}
			raw, _ = json.Marshal(m)
			_ = os.WriteFile(path, raw, 0600)
			if _, err := Inspect(root); err == nil {
				t.Fatal("accepted " + variant)
			}
		})
	}
}

func TestLauncherEscapesPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ownward ' \" $ ` %f")
	linux, err := LinuxLauncher(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(linux), "%%f") || !strings.Contains(string(linux), "\\$") {
		t.Fatal(string(linux))
	}
	script, _, err := MacLauncher(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "'\"'\"'") {
		t.Fatal(string(script))
	}
}
