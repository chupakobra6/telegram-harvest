package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMakeBuildTracksContentWithPreservedModificationTimes(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, dir := range []string{"cmd", "internal", "fake-bin"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("Makefile", string(makefile))
	write("cmd/main.go", "package main\n")
	write("go.mod", "module fixture\n")
	write("go.sum", "fixture checksum\n")
	fakeGo := filepath.Join(root, "fake-bin", "go")
	if err := os.WriteFile(fakeGo, []byte(`#!/bin/sh
printf 'build\n' >> "$HARVEST_TEST_BUILD_LOG"
if [ -f "$HARVEST_TEST_BUILD_FAILURE" ]; then exit 1; fi
while [ "$#" -gt 0 ]; do
  if [ "$1" = '-o' ]; then
    shift
    printf 'fixture binary\n' > "$1"
    exit 0
  fi
  shift
done
exit 1
`), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "build.log")
	failurePath := filepath.Join(root, "fail-build")
	command := func() *exec.Cmd {
		cmd := exec.Command("make", "build")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(fakeGo)+string(os.PathListSeparator)+os.Getenv("PATH"), "HARVEST_TEST_BUILD_LOG="+logPath, "HARVEST_TEST_BUILD_FAILURE="+failurePath)
		return cmd
	}
	build := func(wantCalls int) {
		t.Helper()
		if output, err := command().CombinedOutput(); err != nil {
			t.Fatalf("make build: %v\n%s", err, output)
		}
		log, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(log), "build\n"); got != wantCalls {
			t.Fatalf("build calls = %d, want %d", got, wantCalls)
		}
	}
	build(1)
	build(1)
	stamps, err := filepath.Glob(filepath.Join(root, "bin", ".go-sources-*"))
	if err != nil || len(stamps) != 1 {
		t.Fatalf("build stamps = %v, err=%v", stamps, err)
	}
	future := time.Now().Add(10 * time.Second)
	if err := os.Chtimes(stamps[0], future, future); err != nil {
		t.Fatal(err)
	}
	future = future.Add(10 * time.Second)
	if err := os.Chtimes(filepath.Join(root, "cmd", "main.go"), future, future); err != nil {
		t.Fatal(err)
	}
	build(1)
	for i, path := range []string{"cmd/main.go", "go.mod", "go.sum"} {
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		write(path, "changed "+path+"\n")
		if err := os.Chtimes(filepath.Join(root, path), time.Now(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		build(i + 2)
		build(i + 2)
	}
	write("internal/new.go", "package fixture\n")
	build(5)
	build(5)
	if err := os.Remove(filepath.Join(root, "internal/new.go")); err != nil {
		t.Fatal(err)
	}
	build(6)
	build(6)
	if err := os.Remove(filepath.Join(root, "bin", "telegram-harvest")); err != nil {
		t.Fatal(err)
	}
	build(7)
	write("cmd/main.go", "another source change\n")
	write("fail-build", "fail\n")
	if output, err := command().CombinedOutput(); err == nil {
		t.Fatalf("failed compiler was accepted: %s", output)
	}
	if err := os.Remove(failurePath); err != nil {
		t.Fatal(err)
	}
	build(9)
	build(9)
}
