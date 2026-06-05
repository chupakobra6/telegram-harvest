package harvest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneDailyStateKeepsRetentionWindow(t *testing.T) {
	root := t.TempDir()
	writeRetentionFile(t, filepath.Join(root, "days", "2026-05-20.jsonl"), "old")
	writeRetentionFile(t, filepath.Join(root, "days", "2026-05-20.md"), "old")
	writeRetentionFile(t, filepath.Join(root, "days", "2026-06-01.jsonl"), "new")
	writeRetentionFile(t, filepath.Join(root, "reports", "jsonl", "2026-05-20.jsonl"), "old")
	writeRetentionFile(t, filepath.Join(root, "reports", "md", "2026-05-20.md"), "old")
	writeRetentionFile(t, filepath.Join(root, "reports", "jsonl", "2026-06-01.jsonl"), "new")
	writeRetentionFile(t, filepath.Join(root, "reports", "md", "2026-06-01.md"), "new")
	writeRetentionFile(t, filepath.Join(root, "media", "Chat", "2026-05-20", "photo.jpg"), "old")
	writeRetentionFile(t, filepath.Join(root, "media", "Chat", "2026-06-01", "photo.jpg"), "new")
	writeRetentionFile(t, filepath.Join(root, "transcripts", "Chat", "2026-05-20", "voice.txt"), "old")
	writeRetentionFile(t, filepath.Join(root, "transcripts", "Chat", "2026-06-01", "voice.txt"), "new")
	cacheOld := filepath.Join(root, "transcripts", "cache", "aa", "old.txt")
	cacheNew := filepath.Join(root, "transcripts", "cache", "bb", "new.txt")
	writeRetentionFile(t, cacheOld, "old")
	writeRetentionFile(t, cacheNew, "new")
	oldTime := time.Date(2026, 5, 20, 12, 0, 0, 0, moscowLocation)
	if err := os.Chtimes(cacheOld, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	stats, err := PruneDailyState(DailyRetentionOptions{
		StateDir:   root,
		RetainDays: 7,
		Now:        time.Date(2026, 6, 5, 12, 0, 0, 0, moscowLocation),
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.DeletedFiles == 0 || stats.DeletedDirs == 0 {
		t.Fatalf("expected deletions, got %+v", stats)
	}
	assertMissing(t, filepath.Join(root, "days", "2026-05-20.jsonl"))
	assertMissing(t, filepath.Join(root, "days", "2026-05-20.md"))
	assertExists(t, filepath.Join(root, "days", "2026-06-01.jsonl"))
	assertMissing(t, filepath.Join(root, "reports", "jsonl", "2026-05-20.jsonl"))
	assertMissing(t, filepath.Join(root, "reports", "md", "2026-05-20.md"))
	assertExists(t, filepath.Join(root, "reports", "jsonl", "2026-06-01.jsonl"))
	assertExists(t, filepath.Join(root, "reports", "md", "2026-06-01.md"))
	assertMissing(t, filepath.Join(root, "media", "Chat", "2026-05-20"))
	assertExists(t, filepath.Join(root, "media", "Chat", "2026-06-01", "photo.jpg"))
	assertMissing(t, filepath.Join(root, "transcripts", "Chat", "2026-05-20"))
	assertExists(t, filepath.Join(root, "transcripts", "Chat", "2026-06-01", "voice.txt"))
	assertMissing(t, cacheOld)
	assertExists(t, cacheNew)
}

func writeRetentionFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be missing, err=%v", path, err)
	}
}
