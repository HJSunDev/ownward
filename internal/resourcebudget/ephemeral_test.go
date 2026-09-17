package resourcebudget

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
)

func TestInvalidationCanCloseFileDuringUse(t *testing.T) {
	b, _ := New(32*1024, 0)
	d := NewDisk(MiB)
	f, e := BufferedTempFile(WithDisk(context.Background(), d), t.TempDir(), "race-", MiB, b)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if _, e := f.WriteAt(make([]byte, 100), int64(i*100)); e != nil {
					if !errors.Is(e, os.ErrClosed) {
						t.Error(e)
					}
					return
				}
				f.ReadAt(make([]byte, 100), 0)
			}
		}()
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	wg.Wait()
	if b.Used() != 0 || d.Used() != 0 {
		t.Fatal("closed file retained budget")
	}
}
