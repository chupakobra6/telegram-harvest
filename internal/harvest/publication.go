package harvest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chupakobra6/telegram-harvest/internal/runlock"
)

// A reader with an arbitrary --in can detect a suffix whose checkpoint has
// not been committed, including after the writing process has exited.
func publicationMarker(path string) (string, error) {
	canonical, err := runlock.CanonicalPath(path)
	return canonical + ".harvest-pending", err
}

func EnsurePublished(path string) error {
	return checkPublicationOwner(path, "")
}

func checkPublicationOwner(path, statePath string) error {
	if path == "" {
		return nil
	}
	marker, err := publicationMarker(path)
	if err != nil {
		return err
	}
	owner, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if statePath != "" {
		canonical, err := runlock.CanonicalPath(statePath)
		if err != nil {
			return err
		}
		if string(owner) == canonical {
			return nil
		}
	}
	return fmt.Errorf("output has an unfinished publication; resume its sync before using %s", path)
}

func beginPublication(statePath string, paths ...string) error {
	owner, err := runlock.CanonicalPath(statePath)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		if err := checkPublicationOwner(path, statePath); err != nil {
			return err
		}
		marker, err := publicationMarker(path)
		if err != nil {
			return err
		}
		if err := writePrivateAtomic(marker, []byte(owner)); err != nil {
			return err
		}
	}
	return nil
}

func finishPublication(paths ...string) error {
	for _, path := range paths {
		if path == "" {
			continue
		}
		marker, err := publicationMarker(path)
		if err != nil {
			return err
		}
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		dir, err := os.Open(filepath.Dir(marker))
		if err != nil {
			return err
		}
		err = errors.Join(dir.Sync(), dir.Close())
		if err != nil {
			return err
		}
	}
	return nil
}
