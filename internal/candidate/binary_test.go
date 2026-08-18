package candidate

import (
	"context"
	"fmt"
	"os"
	"testing"
)

const helperVersion = "candidate-test-version"

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(helperVersion)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestInspectBindsExecutableVersionAndDigest(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	result, err := Inspect(context.Background(), path, helperVersion)
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != helperVersion || len(result.SHA256) != 64 || result.Size <= 0 {
		t.Fatalf("invalid candidate inspection: %#v", result)
	}
}

func TestInspectRejectsCandidateMismatch(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), path, "another-version"); err == nil {
		t.Fatal("candidate mismatch was accepted")
	}
}
