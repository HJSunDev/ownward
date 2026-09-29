package config

import (
	"github.com/HJSunDev/ownward/internal/desktop"
	"os"
	"path/filepath"
	"testing"
)

func TestTargetPrecedenceAndDamagedBinding(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("HOME", root)
	t.Setenv("OWNWARD_DATA_DIR", "")
	path, err := desktop.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	bound := filepath.Join(root, "selected")
	if err := desktop.Save(path, desktop.State{Release: "v1", Executable: filepath.Join(root, "ownward"), Binding: &desktop.Binding{System: "s1", Data: bound}}); err != nil {
		t.Fatal(err)
	}
	c, err := Load("")
	if err != nil || c.DataDir != bound || c.System != "s1" {
		t.Fatal(c, err)
	}
	env := filepath.Join(root, "env")
	t.Setenv("OWNWARD_DATA_DIR", env)
	c, err = Load("")
	if err != nil || c.DataDir != env || c.System != "" {
		t.Fatal(c, err)
	}
	explicit := filepath.Join(root, "explicit")
	c, err = Load(explicit)
	if err != nil || c.DataDir != explicit {
		t.Fatal(c, err)
	}
	t.Setenv("OWNWARD_DATA_DIR", "")
	_ = os.WriteFile(path, []byte("broken"), 0600)
	if _, err = Load(""); err == nil {
		t.Fatal("corrupt binding silently selected another directory")
	}
	if _, err = Load(explicit); err != nil {
		t.Fatal("explicit location blocked by unrelated metadata", err)
	}
}
