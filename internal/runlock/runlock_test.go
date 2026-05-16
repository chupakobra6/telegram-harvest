package runlock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquirePreventsSecondRuntimeAndReleaseRemovesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected lock file: %v", err)
	}

	second, err := Acquire(path)
	if err == nil {
		_ = second.Release()
		t.Fatalf("expected second acquire to fail")
	}
	if !errors.Is(err, ErrAlreadyLocked) {
		t.Fatalf("expected ErrAlreadyLocked, got %v", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected lock file removed, stat err=%v", err)
	}

	third, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatalf("release third lock: %v", err)
	}
}

func TestReleaseNilHandleIsNoop(t *testing.T) {
	var handle *Handle
	if err := handle.Release(); err != nil {
		t.Fatalf("nil release returned error: %v", err)
	}
}

func TestAcquireRejectsEmptyPath(t *testing.T) {
	if _, err := Acquire(""); err == nil {
		t.Fatalf("expected empty path error")
	}
}
