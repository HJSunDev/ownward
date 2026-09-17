package resourcebudget

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
)

func TestBufferedFileSpillPreservesOffsetsAndQuota(t *testing.T) {
	b, _ := New(32*1024, 0)
	disk := NewDisk(MiB)
	ctx := WithDisk(context.Background(), disk)
	dir := t.TempDir()
	f, e := BufferedTempFile(ctx, dir, "fixture", MiB, b)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write([]byte("abc")); e != nil {
		t.Fatal(e)
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 || b.Used() != 8192 || disk.Used() != 3 {
		t.Fatal("small payload must stay bounded in memory")
	}
	if _, e = f.WriteAt([]byte("z"), 9000); e != nil {
		t.Fatal(e)
	}
	if f.File == nil || b.Used() != 0 || disk.Used() != 9001 {
		t.Fatal("spill/quota mismatch")
	}
	if _, e = f.Write([]byte("d")); e != nil {
		t.Fatal(e)
	}
	f.Seek(0, io.SeekStart)
	var out bytes.Buffer
	if _, e = io.Copy(&out, f); e != nil {
		t.Fatal(e)
	}
	if out.Len() != 9001 || string(out.Bytes()[:4]) != "abcd" || out.Bytes()[9000] != 'z' {
		t.Fatal("contents or sequential offset changed")
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	f.Close()
	if files, _ := os.ReadDir(dir); len(files) != 0 || b.Used() != 0 || disk.Used() != 0 {
		t.Fatal("resources leaked")
	}
	if _, e = f.Write([]byte("x")); !errors.Is(e, os.ErrClosed) {
		t.Fatal(e)
	}
}

func TestBufferedFileBudgetFallbackAndFailure(t *testing.T) {
	b, _ := New(8192, 0)
	dir := t.TempDir()
	disk := NewDisk(10)
	ctx := WithDisk(context.Background(), disk)
	a, e := BufferedTempFile(ctx, dir, "first", 10, b)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	z, e := BufferedTempFile(ctx, dir, "second", 10, b)
	if e != nil {
		t.Fatal(e)
	}
	defer z.Close()
	if a.File != nil || z.File == nil {
		t.Fatal("optional buffer must fall back without waiting")
	}
	if _, e = a.Write([]byte("123456")); e != nil {
		t.Fatal(e)
	}
	if _, e = z.Write([]byte("12345")); e == nil {
		t.Fatal("shared quota bypassed")
	}
	if _, e = a.WriteAt([]byte("x"), -1); e == nil {
		t.Fatal("negative offset accepted")
	}
	a.Close()
	z.Close()
	if b.Used() != 0 || disk.Used() != 0 {
		t.Fatal("failed write leaked budget")
	}
}
