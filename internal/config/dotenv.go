package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// LoadDotEnv loads KEY=VALUE pairs from path into the process environment.
// Existing environment variables win over values from the file.
// Missing files are ignored.
func LoadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		if strings.HasPrefix(raw, "export ") {
			raw = strings.TrimSpace(strings.TrimPrefix(raw, "export "))
		}
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNo)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("%s:%d: empty key", path, lineNo)
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		parsed, err := parseDotEnvValue(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		if err := os.Setenv(key, parsed); err != nil {
			return fmt.Errorf("%s:%d: set %s: %w", path, lineNo, key, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan %s: %w", path, err)
	}
	return nil
}

func parseDotEnvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		return strconv.Unquote(raw)
	}
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		return raw[1 : len(raw)-1], nil
	}
	if idx := strings.Index(raw, " #"); idx >= 0 {
		return strings.TrimSpace(raw[:idx]), nil
	}
	return raw, nil
}
