package runlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

var ErrAlreadyLocked = errors.New("another telegram-harvest operation owns this resource")

type Handle struct {
	file *os.File
	path string
}

func Acquire(path string) (*Handle, error) {
	if path == "" {
		return nil, fmt.Errorf("runtime lock path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("prepare runtime lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open runtime lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyLocked, path)
		}
		return nil, fmt.Errorf("acquire runtime lock: %w", err)
	}
	return &Handle{file: file, path: path}, nil
}

func (h *Handle) Release() error {
	if h == nil || h.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(h.file.Fd()), syscall.LOCK_UN)
	closeErr := h.file.Close()
	h.file = nil
	if unlockErr != nil {
		return fmt.Errorf("release runtime lock %s: %w", h.path, unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close runtime lock %s: %w", h.path, closeErr)
	}
	// The name must keep pointing to the same inode across owners. Unlinking
	// after unlock lets a successor and a newly created file both be locked.
	return nil
}

// CanonicalPath resolves aliases even when the output does not exist yet.
func CanonicalPath(path string) (string, error) {
	return canonicalPath(path, 0)
}

func canonicalPath(path string, links int) (string, error) {
	if links > 255 {
		return "", fmt.Errorf("too many symbolic links in %s", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, tail := abs, ""
	for {
		resolved, err := filepath.EvalSymlinks(parent)
		if err == nil {
			return filepath.Join(resolved, tail), nil
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(parent) == parent {
			return "", err
		}
		// A generated directory may briefly be absent during rebuild. Resolve
		// its symlink anyway so aliases keep using the already owned lock.
		if target, linkErr := os.Readlink(parent); linkErr == nil {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(parent), target)
			}
			return canonicalPath(filepath.Join(target, tail), links+1)
		}
		tail = filepath.Join(filepath.Base(parent), tail)
		parent = filepath.Dir(parent)
	}
}

type Resources struct{ handles []*Handle }

// AcquireResources owns each actual input/output independently of the profile.
// Sidecars also survive atomic replacement of the data or generated directory.
func AcquireResources(paths ...string) (*Resources, error) {
	unique := make(map[string]struct{})
	for _, path := range paths {
		if path == "" {
			continue
		}
		canonical, err := CanonicalPath(path)
		if err != nil {
			return nil, err
		}
		unique[canonical+".harvest.lock"] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for path := range unique {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	resources := &Resources{}
	for _, path := range ordered {
		handle, err := Acquire(path)
		if err != nil {
			return nil, errors.Join(err, resources.Release())
		}
		resources.handles = append(resources.handles, handle)
	}
	return resources, nil
}

func (r *Resources) Release() error {
	if r == nil {
		return nil
	}
	var err error
	for i := len(r.handles) - 1; i >= 0; i-- {
		err = errors.Join(err, r.handles[i].Release())
	}
	r.handles = nil
	return err
}
