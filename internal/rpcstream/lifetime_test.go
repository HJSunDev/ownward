package rpcstream

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

func TestResultRevokedBeforeBuildNeverStarts(t *testing.T) {
	b, _ := resourcebudget.New(2*resourcebudget.MiB, 0)
	s := New(t.TempDir(), b, resourcebudget.MiB)
	defer s.Close()
	_, c, e := s.Project(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ownward_read","arguments":{"id":"a"}}}`))
	if e != nil {
		t.Fatal(e)
	}
	wrote := false
	_, e = c.Result(context.Background(), func(w io.Writer) error { wrote = true; _, e := io.WriteString(w, `{}`); return e }, func() error { return nil }, func(close func() error) (func() error, error) {
		if e := close(); e != nil {
			return nil, e
		}
		return func() error { return nil }, nil
	})
	if e == nil || wrote || s.Disk.Used() != 0 {
		t.Fatal("revoked construction started or retained input", e, wrote, s.Disk.Used())
	}
}

func TestResultCopyFailureReleasesRegistration(t *testing.T) {
	b, _ := resourcebudget.New(2*resourcebudget.MiB, 0)
	s := New(t.TempDir(), b, resourcebudget.MiB)
	defer s.Close()
	_, c, e := s.Project(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ownward_read","arguments":{"id":"a"}}}`))
	if e != nil {
		t.Fatal(e)
	}
	released := 0
	_, e = c.Result(context.Background(), func(w io.Writer) error {
		io.WriteString(w, strings.Repeat("x", 70000))
		return errors.New("copy failed")
	}, func() error { return nil }, func(close func() error) (func() error, error) {
		var once sync.Once
		return func() error { once.Do(func() { released++; close() }); return nil }, nil
	})
	if e == nil || released != 1 || s.Disk.Used() != 0 {
		t.Fatal("failed copy retained registration or bytes", e, released, s.Disk.Used())
	}
}
