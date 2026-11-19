package boundedstore

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLockDirectoryRejectsConcurrentOwnerUntilClosed(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".ownward.lock")
	first, err := LockDirectory(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockDirectory(lockPath); err == nil || !strings.Contains(err.Error(), "另一个 Ownward 进程") {
		t.Fatalf("expected exclusive lock conflict, got %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := LockDirectory(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}
