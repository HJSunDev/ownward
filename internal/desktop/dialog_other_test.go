//go:build !windows

package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLinuxNativeChoiceProtocol(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("zenity response protocol")
	}
	root := t.TempDir()
	// Exercise the actual process boundary, including zenity's nonzero extra-button status.
	if err := os.WriteFile(filepath.Join(root, "zenity"), []byte("#!/bin/sh\nprintf '%s' \"$OWNWARD_TEST_CHOICE\"\nexit \"$OWNWARD_TEST_EXIT\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DISPLAY", ":test")
	for _, tc := range []struct {
		name, output, status string
		choice               int
		failed               bool
	}{
		{"primary", "", "0", 1, false},
		{"secondary", "打开已有资料\n", "127", 2, false},
		{"cancel", "", "1", 0, false},
		{"failure", "", "3", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OWNWARD_TEST_CHOICE", tc.output)
			t.Setenv("OWNWARD_TEST_EXIT", tc.status)
			choice, err := Choose("Ownward", "开始使用", "开始使用", "打开已有资料")
			if choice != tc.choice || (err != nil) != tc.failed {
				t.Fatalf("choice=%d error=%v", choice, err)
			}
		})
	}
}

func TestLinuxProgressDistinguishesCancellationFromDialogFailure(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("zenity response protocol")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "zenity"), []byte("#!/bin/sh\nexit \"$OWNWARD_TEST_EXIT\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DISPLAY", ":test")
	for _, status := range []string{"1", "3"} {
		t.Run(status, func(t *testing.T) {
			t.Setenv("OWNWARD_TEST_EXIT", status)
			err := WithProgress(context.Background(), func(ctx context.Context, _ func(string)) error {
				<-ctx.Done()
				return errors.New("interrupted worker")
			})
			if err == nil || errors.Is(err, context.Canceled) != (status == "1") {
				t.Fatalf("dialog status %s reported as %v", status, err)
			}
		})
	}
}
