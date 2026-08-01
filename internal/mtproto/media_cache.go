package mtproto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
)

const (
	mediaCacheVersion    = 1
	mediaCacheRetention  = 30 * 24 * time.Hour
	mediaCacheTempMaxAge = 24 * time.Hour
)

type persistentMediaCache struct {
	root      string
	retention time.Duration
	now       func() time.Time

	mu     sync.Mutex
	pruned bool
}

type mediaCacheMetadata struct {
	Version        int       `json:"version"`
	IdentitySHA256 string    `json:"identity_sha256"`
	ContentSHA256  string    `json:"content_sha256"`
	Size           int64     `json:"size"`
	LastUsedAt     time.Time `json:"last_used_at"`
}

type mediaCacheEntryPaths struct {
	data     string
	metadata string
}

func newPersistentMediaCache(root string) *persistentMediaCache {
	return &persistentMediaCache{
		root:      filepath.Clean(root),
		retention: mediaCacheRetention,
		now:       time.Now,
	}
}

func (s *Session) persistentMediaCache(mediaDir string) *persistentMediaCache {
	root := mediaCacheRoot(mediaDir)
	s.mediaCacheMu.Lock()
	defer s.mediaCacheMu.Unlock()
	if s.mediaCaches == nil {
		s.mediaCaches = make(map[string]*persistentMediaCache)
	}
	if cache := s.mediaCaches[root]; cache != nil {
		return cache
	}
	cache := newPersistentMediaCache(root)
	s.mediaCaches[root] = cache
	return cache
}

func mediaCacheRoot(mediaDir string) string {
	clean := filepath.Clean(mediaDir)
	return filepath.Join(filepath.Dir(clean), ".media-cache")
}

func mediaCacheIdentity(attachment harvest.Attachment) (string, bool) {
	mediaID := strings.TrimSpace(attachment.MediaID)
	if mediaID == "" {
		return "", false
	}
	switch attachment.Kind {
	case "photo", "image", "document":
		return attachment.Kind + "\x00" + mediaID, true
	default:
		return "", false
	}
}

func (c *persistentMediaCache) NewDownloadPath(fileName string) (string, error) {
	if c == nil {
		return "", errors.New("media cache is nil")
	}
	tempDir := filepath.Join(c.root, ".tmp")
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return "", fmt.Errorf("prepare media cache temp dir: %w", err)
	}
	extension := filepath.Ext(safeFileName(fileName))
	file, err := os.CreateTemp(tempDir, "download-*"+extension)
	if err != nil {
		return "", fmt.Errorf("create media cache temp file: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close media cache temp file: %w", err)
	}
	return path, nil
}

func (c *persistentMediaCache) Restore(identity, target string, expectedSize int64) (bool, error) {
	if c == nil || strings.TrimSpace(identity) == "" {
		return false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensurePrunedLocked()

	identityHash := mediaCacheIdentityHash(identity)
	paths := c.entryPaths(identityHash)
	metadata, valid, err := c.loadValidEntryLocked(paths, identityHash, expectedSize)
	if err != nil || !valid {
		return false, err
	}
	if err := publishMediaFileAtomic(paths.data, target); err != nil {
		return false, fmt.Errorf("publish cached media: %w", err)
	}
	metadata.LastUsedAt = c.now().UTC()
	_ = writeMediaCacheMetadataAtomic(paths.metadata, metadata)
	return true, nil
}

func (c *persistentMediaCache) Store(identity, source, target string, expectedSize int64) error {
	if c == nil || strings.TrimSpace(identity) == "" {
		return errors.New("media cache identity is empty")
	}
	size, contentHash, err := mediaFileIdentity(source)
	if err != nil {
		return fmt.Errorf("inspect downloaded media: %w", err)
	}
	if expectedSize > 0 && size != expectedSize {
		return fmt.Errorf("downloaded media size = %d, expected %d", size, expectedSize)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensurePrunedLocked()
	identityHash := mediaCacheIdentityHash(identity)
	paths := c.entryPaths(identityHash)
	if err := os.MkdirAll(filepath.Dir(paths.data), 0o700); err != nil {
		return fmt.Errorf("prepare media cache entry dir: %w", err)
	}
	if err := publishMediaFileAtomic(source, paths.data); err != nil {
		return fmt.Errorf("publish media cache data: %w", err)
	}
	metadata := mediaCacheMetadata{
		Version:        mediaCacheVersion,
		IdentitySHA256: identityHash,
		ContentSHA256:  contentHash,
		Size:           size,
		LastUsedAt:     c.now().UTC(),
	}
	if err := writeMediaCacheMetadataAtomic(paths.metadata, metadata); err != nil {
		_ = os.Remove(paths.data)
		return fmt.Errorf("publish media cache metadata: %w", err)
	}
	if err := publishMediaFileAtomic(paths.data, target); err != nil {
		return fmt.Errorf("publish cached media: %w", err)
	}
	return nil
}

func (c *persistentMediaCache) Prune() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruned = true
	return c.pruneLocked()
}

func (c *persistentMediaCache) ensurePrunedLocked() {
	if c.pruned {
		return
	}
	c.pruned = true
	_ = c.pruneLocked()
}

func (c *persistentMediaCache) pruneLocked() error {
	if strings.TrimSpace(c.root) == "" {
		return nil
	}
	now := c.now()
	err := filepath.WalkDir(c.root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == ".tmp" {
			info, err := entry.Info()
			if err == nil && now.Sub(info.ModTime()) > mediaCacheTempMaxAge {
				_ = os.Remove(path)
			}
			return nil
		}
		extension := filepath.Ext(path)
		switch extension {
		case ".json":
			metadata, err := readMediaCacheMetadata(path)
			paths := mediaCacheEntryPaths{
				data:     strings.TrimSuffix(path, extension) + ".data",
				metadata: path,
			}
			if err != nil || metadata.Version != mediaCacheVersion || metadata.LastUsedAt.IsZero() {
				c.removeEntry(paths)
				return nil
			}
			if now.Sub(metadata.LastUsedAt) > c.retention {
				c.removeEntry(paths)
			}
		case ".data":
			metadataPath := strings.TrimSuffix(path, extension) + ".json"
			if _, err := os.Stat(metadataPath); !errors.Is(err, os.ErrNotExist) {
				return nil
			}
			info, err := entry.Info()
			if err == nil && now.Sub(info.ModTime()) > c.retention {
				_ = os.Remove(path)
			}
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (c *persistentMediaCache) loadValidEntryLocked(
	paths mediaCacheEntryPaths,
	identityHash string,
	expectedSize int64,
) (mediaCacheMetadata, bool, error) {
	metadata, err := readMediaCacheMetadata(paths.metadata)
	if errors.Is(err, os.ErrNotExist) {
		return mediaCacheMetadata{}, false, nil
	}
	if err != nil {
		c.removeEntry(paths)
		return mediaCacheMetadata{}, false, nil
	}
	if metadata.Version != mediaCacheVersion ||
		metadata.IdentitySHA256 != identityHash ||
		metadata.Size <= 0 ||
		(expectedSize > 0 && metadata.Size != expectedSize) {
		c.removeEntry(paths)
		return mediaCacheMetadata{}, false, nil
	}
	size, contentHash, err := mediaFileIdentity(paths.data)
	if errors.Is(err, os.ErrNotExist) {
		c.removeEntry(paths)
		return mediaCacheMetadata{}, false, nil
	}
	if err != nil {
		return mediaCacheMetadata{}, false, err
	}
	if size != metadata.Size || contentHash != metadata.ContentSHA256 {
		c.removeEntry(paths)
		return mediaCacheMetadata{}, false, nil
	}
	return metadata, true, nil
}

func (c *persistentMediaCache) entryPaths(identityHash string) mediaCacheEntryPaths {
	base := filepath.Join(c.root, identityHash[:2], identityHash)
	return mediaCacheEntryPaths{data: base + ".data", metadata: base + ".json"}
}

func (c *persistentMediaCache) removeEntry(paths mediaCacheEntryPaths) {
	_ = os.Remove(paths.metadata)
	_ = os.Remove(paths.data)
}

func mediaCacheIdentityHash(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

func mediaFileIdentity(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return 0, "", copyErr
	}
	if closeErr != nil {
		return 0, "", closeErr
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

func readMediaCacheMetadata(path string) (mediaCacheMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return mediaCacheMetadata{}, err
	}
	var metadata mediaCacheMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return mediaCacheMetadata{}, err
	}
	return metadata, nil
}

func writeMediaCacheMetadataAtomic(path string, metadata mediaCacheMetadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".metadata-*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tempPath)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return syncMediaDirectory(filepath.Dir(path))
}

func publishMediaFileAtomic(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tempFile, err := os.CreateTemp(filepath.Dir(target), ".media-*.tmp")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Remove(tempPath); err != nil {
		return err
	}
	defer os.Remove(tempPath)
	if err := cloneMediaFile(source, tempPath); err != nil {
		if err := os.Link(source, tempPath); err != nil {
			if err := copyMediaFile(source, tempPath); err != nil {
				return err
			}
		}
	}
	if err := os.Rename(tempPath, target); err != nil {
		return err
	}
	return syncMediaDirectory(filepath.Dir(target))
}

func copyMediaFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cleanup := func() {
		_ = output.Close()
		_ = os.Remove(target)
	}
	if _, err := io.Copy(output, input); err != nil {
		cleanup()
		return err
	}
	if err := output.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(target)
		return err
	}
	return nil
}

func syncMediaDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
