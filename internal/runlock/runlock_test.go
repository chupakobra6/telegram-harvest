package runlock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestAcquirePreventsSecondRuntimeAndPreservesLockIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
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
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock file identity changed: %v", err)
	}

	third, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatalf("release third lock: %v", err)
	}
}

func TestLockHandoffDoesNotSplitOwnershipAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Release() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	waiter := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLockProcessHelper$")
	waiter.Env = append(os.Environ(), "HARVEST_LOCK_HELPER=waiter", "HARVEST_LOCK_PATH="+path)
	input, err := waiter.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := waiter.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	waiter.Stderr = os.Stderr
	if err := waiter.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = waiter.Process.Kill(); _ = waiter.Wait() })
	reader := bufio.NewReader(output)
	if line, err := reader.ReadString('\n'); err != nil || line != "opened\n" {
		t.Fatalf("waiter open: %q %v", line, err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(input, "acquire"); err != nil {
		t.Fatal(err)
	}
	if line, err := reader.ReadString('\n'); err != nil || line != "owned\n" {
		t.Fatalf("waiter acquire: %q %v", line, err)
	}
	probe := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLockProcessHelper$")
	probe.Env = append(os.Environ(), "HARVEST_LOCK_HELPER=probe", "HARVEST_LOCK_PATH="+path)
	if data, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("third process acquired an already owned path: %s %v", data, err)
	}
}

func TestLockProcessHelper(t *testing.T) {
	mode, path := os.Getenv("HARVEST_LOCK_HELPER"), os.Getenv("HARVEST_LOCK_PATH")
	if mode == "" {
		return
	}
	if mode == "probe" {
		lock, err := Acquire(path)
		if lock != nil {
			_ = lock.Release()
		}
		if !errors.Is(err, ErrAlreadyLocked) {
			t.Fatalf("expected occupied lock: %v", err)
		}
		return
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fmt.Println("opened")
	reader := bufio.NewReader(os.Stdin)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	fmt.Println("owned")
	_, _ = reader.ReadString('\n')
}

func TestResourcesResolveAliasesAndReleasePartialAcquisition(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	last := filepath.Join(dir, "z.jsonl")
	owner, err := AcquireResources(last)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	first := filepath.Join(dir, "a.jsonl")
	if lock, err := AcquireResources(first, filepath.Join(alias, "z.jsonl")); !errors.Is(err, ErrAlreadyLocked) {
		_ = lock.Release()
		t.Fatalf("alias bypassed ownership: %v", err)
	}
	independent, err := AcquireResources(first)
	if err != nil {
		t.Fatalf("failed acquisition retained independent resource: %v", err)
	}
	if err := independent.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestResourcesResolveDanglingDirectoryAlias(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "generated")
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink("generated", alias); err != nil {
		t.Fatal(err)
	}
	owner, err := AcquireResources(target)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	if other, err := AcquireResources(alias); !errors.Is(err, ErrAlreadyLocked) {
		_ = other.Release()
		t.Fatalf("dangling alias bypassed owner: %v", err)
	}
	got, err := CanonicalPath(filepath.Join(alias, "nested", "new.jsonl"))
	want, wantErr := CanonicalPath(filepath.Join(target, "nested", "new.jsonl"))
	if err != nil || wantErr != nil || got != want {
		t.Fatalf("uncreated descendant alias=%q target=%q err=%v/%v", got, want, err, wantErr)
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
