package runlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var ErrAlreadyLocked = errors.New("another telegram-harvest runtime is active for this session")

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
	_ = os.Remove(h.path)
	return nil
}
