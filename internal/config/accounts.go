package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const accountProfileVersion = 1

type Account struct {
	Version    int    `json:"version"`
	Name       string `json:"name"`
	APIProfile string `json:"api_profile"`
	AccountID  int64  `json:"account_id,omitempty"`
}

func AccountStoreDir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate account directory: %w", err)
	}
	return filepath.Join(root, "telegram-harvest", "accounts"), nil
}

func CreateAccount(name, apiProfile string) (Account, string, error) {
	root, err := AccountStoreDir()
	if err != nil {
		return Account{}, "", err
	}
	return createAccountAt(root, name, apiProfile)
}

func ReadAccount(name string) (Account, string, error) {
	root, err := AccountStoreDir()
	if err != nil {
		return Account{}, "", err
	}
	return readAccountAt(root, name)
}

func ListAccounts() ([]Account, error) {
	root, err := AccountStoreDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := checkPrivateDir(root); err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		account, _, err := readAccountAt(root, entry.Name())
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Name < accounts[j].Name })
	return accounts, nil
}

func BindAccountID(name string, accountID int64) error {
	root, err := AccountStoreDir()
	if err != nil {
		return err
	}
	return bindAccountIDAt(root, name, accountID)
}

func validateAccountName(name string) error {
	if name == "main" || name == "study" || len(name) < 1 || len(name) > 48 {
		return fmt.Errorf("account name must be 1–48 lowercase Latin letters, digits, hyphens or underscores, and cannot be main or study")
	}
	for i, r := range name {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || (i > 0 && (r == '-' || r == '_'))
		if !valid {
			return fmt.Errorf("invalid account name %q: use lowercase Latin letters, digits, hyphens or underscores", name)
		}
	}
	return nil
}

func createAccountAt(root, name, apiProfile string) (Account, string, error) {
	if err := validateAccountName(name); err != nil {
		return Account{}, "", err
	}
	if apiProfile != "main" && apiProfile != "study" {
		return Account{}, "", fmt.Errorf("--api-profile must be main or study")
	}
	parent := filepath.Dir(root)
	if err := ensurePrivateDir(parent); err != nil {
		return Account{}, "", err
	}
	if err := ensurePrivateDir(root); err != nil {
		return Account{}, "", err
	}
	dir := filepath.Join(root, name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Account{}, "", fmt.Errorf("account %q already exists", name)
		}
		return Account{}, "", err
	}
	account := Account{Version: accountProfileVersion, Name: name, APIProfile: apiProfile}
	if err := writeAccount(dir, account); err != nil {
		_ = os.Remove(dir)
		return Account{}, "", err
	}
	return account, dir, nil
}

func readAccountAt(root, name string) (Account, string, error) {
	if err := validateAccountName(name); err != nil {
		return Account{}, "", err
	}
	if err := checkPrivateDir(filepath.Dir(root)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Account{}, "", err
	}
	if err := checkPrivateDir(root); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Account{}, "", err
	}
	dir := filepath.Join(root, name)
	if err := checkPrivateDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Account{}, "", fmt.Errorf("account %q is not registered; run `telegram-harvest account add --name %s --api-profile main`", name, name)
		}
		return Account{}, "", err
	}
	path := filepath.Join(dir, "profile.json")
	info, err := os.Lstat(path)
	if err != nil {
		return Account{}, "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return Account{}, "", fmt.Errorf("account profile must be a private regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Account{}, "", err
	}
	var account Account
	if err := json.Unmarshal(data, &account); err != nil || account.Version != accountProfileVersion || account.Name != name ||
		(account.APIProfile != "main" && account.APIProfile != "study") || account.AccountID < 0 {
		return Account{}, "", fmt.Errorf("account profile is invalid: %s", path)
	}
	return account, dir, nil
}

func bindAccountIDAt(root, name string, accountID int64) error {
	if accountID <= 0 {
		return fmt.Errorf("telegram account ID must be positive")
	}
	account, dir, err := readAccountAt(root, name)
	if err != nil {
		return err
	}
	if account.AccountID != 0 && account.AccountID != accountID {
		return fmt.Errorf("registered account %q is bound to Telegram ID %d; current session has ID %d", name, account.AccountID, accountID)
	}
	if account.AccountID == accountID {
		return nil
	}
	account.AccountID = accountID
	return writeAccount(dir, account)
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return checkPrivateDir(path)
}

func checkPrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("account directory must be private (mode 0700): %s", path)
	}
	return nil
}

func writeAccount(dir string, account Account) error {
	data, err := json.MarshalIndent(account, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".profile-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(dir, "profile.json"))
}

func (a Account) String() string {
	status := "authorization required"
	if a.AccountID > 0 {
		status = fmt.Sprintf("Telegram ID %d", a.AccountID)
	}
	return strings.Join([]string{a.Name, "API=" + a.APIProfile, status}, "\t")
}
