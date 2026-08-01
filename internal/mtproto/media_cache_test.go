package mtproto

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
)

func TestPersistentMediaCacheRestoresSameMediaToDifferentMessagePaths(t *testing.T) {
	dir := t.TempDir()
	cache := newPersistentMediaCache(filepath.Join(dir, ".media-cache"))
	source := filepath.Join(dir, "download.bin")
	want := []byte("shared telegram media")
	mustWriteMediaCacheTestFile(t, source, want)
	firstTarget := filepath.Join(dir, "media", "first", "original-name.pdf")
	secondTarget := filepath.Join(dir, "media", "second", "renamed-forward.pdf")
	identity := "document\x00document:42"

	if err := cache.Store(identity, source, firstTarget, int64(len(want))); err != nil {
		t.Fatal(err)
	}
	hit, err := cache.Restore(identity, secondTarget, int64(len(want)))
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("expected cache hit for the same Telegram media identity")
	}
	for _, path := range []string{firstTarget, secondTarget} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
	if hit, err := cache.Restore("document\x00document:other", filepath.Join(dir, "other.pdf"), int64(len(want))); err != nil || hit {
		t.Fatalf("different Telegram identity hit = %v err = %v", hit, err)
	}
}

func TestPersistentMediaCacheRejectsCorruptSHAAndSize(t *testing.T) {
	dir := t.TempDir()
	cache := newPersistentMediaCache(filepath.Join(dir, ".media-cache"))
	identity := "photo\x00photo:42:x"
	source := filepath.Join(dir, "download.jpg")
	target := filepath.Join(dir, "media", "first.jpg")
	mustWriteMediaCacheTestFile(t, source, []byte("original"))
	if err := cache.Store(identity, source, target, 8); err != nil {
		t.Fatal(err)
	}
	paths := cache.entryPaths(mediaCacheIdentityHash(identity))
	mustWriteMediaCacheTestFile(t, paths.data, []byte("tampered"))

	hit, err := cache.Restore(identity, filepath.Join(dir, "media", "second.jpg"), 8)
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Fatal("corrupt cache entry must be a miss")
	}
	if _, err := os.Stat(paths.metadata); !os.IsNotExist(err) {
		t.Fatalf("corrupt metadata entry still exists: %v", err)
	}

	mustWriteMediaCacheTestFile(t, source, []byte("fresh-data"))
	if err := cache.Store(identity, source, target, 10); err != nil {
		t.Fatal(err)
	}
	if hit, err := cache.Restore(identity, filepath.Join(dir, "media", "wrong-size.jpg"), 11); err != nil || hit {
		t.Fatalf("size mismatch hit = %v err = %v", hit, err)
	}
}

func TestPersistentMediaCacheTTLDoesNotDeletePublishedReports(t *testing.T) {
	dir := t.TempDir()
	cache := newPersistentMediaCache(filepath.Join(dir, ".media-cache"))
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }
	identity := "document\x00document:7"
	source := filepath.Join(dir, "download.bin")
	target := filepath.Join(dir, "media", "report.bin")
	mustWriteMediaCacheTestFile(t, source, []byte("report survives cache expiry"))
	if err := cache.Store(identity, source, target, 28); err != nil {
		t.Fatal(err)
	}

	now = now.Add(mediaCacheRetention - time.Hour)
	if err := cache.Prune(); err != nil {
		t.Fatal(err)
	}
	refreshedTarget := filepath.Join(dir, "media", "refreshed.bin")
	if hit, err := cache.Restore(identity, refreshedTarget, 28); err != nil || !hit {
		t.Fatalf("pre-expiry restore hit = %v err = %v", hit, err)
	}
	now = now.Add(mediaCacheRetention + time.Second)
	if err := cache.Prune(); err != nil {
		t.Fatal(err)
	}
	if hit, err := cache.Restore(identity, filepath.Join(dir, "media", "expired.bin"), 28); err != nil || hit {
		t.Fatalf("expired restore hit = %v err = %v", hit, err)
	}
	for _, path := range []string{target, refreshedTarget} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "report survives cache expiry" {
			t.Fatalf("published report %s after cache expiry = %q err=%v", path, got, err)
		}
	}
}

func TestMediaCacheIdentityRequiresTelegramIDAndSupportedKind(t *testing.T) {
	for _, tc := range []struct {
		attachment harvest.Attachment
		want       bool
	}{
		{attachment: harvest.Attachment{Kind: "photo", MediaID: "photo:1:x"}, want: true},
		{attachment: harvest.Attachment{Kind: "image", MediaID: "document:2"}, want: true},
		{attachment: harvest.Attachment{Kind: "document", MediaID: "document:3"}, want: true},
		{attachment: harvest.Attachment{Kind: "document", FileName: "same.pdf"}, want: false},
		{attachment: harvest.Attachment{Kind: "video", MediaID: "document:4"}, want: false},
	} {
		_, got := mediaCacheIdentity(tc.attachment)
		if got != tc.want {
			t.Fatalf("mediaCacheIdentity(%+v) ok = %v, want %v", tc.attachment, got, tc.want)
		}
	}
}

func mustWriteMediaCacheTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
