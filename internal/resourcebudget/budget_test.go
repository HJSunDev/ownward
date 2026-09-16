package resourcebudget

import (
	"context"
	"errors"
	"testing"
)

func TestControlReserveAndCancellation(t *testing.T) {
	b, err := New(10, 2)
	if err != nil {
		t.Fatal(err)
	}
	release, err := b.Acquire(context.Background(), 8, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = b.Acquire(ctx, 1, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	control, err := b.Acquire(context.Background(), 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if b.Used() != 10 {
		t.Fatal(b.Used())
	}
	release()
	release()
	control()
	if b.Used() != 0 {
		t.Fatal(b.Used())
	}
	if _, err = b.Acquire(context.Background(), 9, false); err == nil {
		t.Fatal("ordinary work consumed reserved budget")
	}
}
