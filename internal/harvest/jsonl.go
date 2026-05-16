package harvest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func OpenJSONL(path string, appendMode bool) (*json.Encoder, *os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("prepare output dir: %w", err)
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open output: %w", err)
	}
	return json.NewEncoder(file), file, nil
}
