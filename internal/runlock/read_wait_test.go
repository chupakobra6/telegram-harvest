package runlock

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestReadWaitHandoffAndTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account.lock")
	holder, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := AcquireContext(ctx, path); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrAlreadyLocked) {
		t.Fatalf("waiting on a busy account: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		next, err := AcquireContext(ctx, path)
		if err == nil {
			err = next.Release()
		}
		done <- err
	}()
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
