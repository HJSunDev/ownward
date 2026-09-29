package resourcebudget

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckFreeBeforeFileCreation(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "existing")
	if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, file, filepath.Join(root, "storage.json"), filepath.Join(root, "future", "storage.json")} {
		if err := CheckFree(path, 0); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if err := CheckFree(path, ^uint64(0)); err == nil {
			t.Fatalf("accepted impossible reservation: %s", path)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "future")); !os.IsNotExist(err) {
		t.Fatal("space check created directories", err)
	}
}
