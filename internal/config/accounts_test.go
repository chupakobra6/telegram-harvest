package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisteredAccountsHavePrivateSeparateSessionsAndBindings(t *testing.T) {
	clearTelegramConfigEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("TG_HARVEST_DAILY_APP_ID", "77")
	t.Setenv("TG_HARVEST_DAILY_APP_HASH", "main-api-hash")
	for _, name := range []string{"lumina22", "work"} {
		if _, _, err := CreateAccount(name, "main"); err != nil {
			t.Fatal(err)
		}
	}
	if err := BindAccountID("lumina22", 101); err != nil {
		t.Fatal(err)
	}
	if err := BindAccountID("work", 202); err != nil {
		t.Fatal(err)
	}
	lumina, err := LoadProfile("lumina22")
	if err != nil {
		t.Fatal(err)
	}
	work, err := LoadProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	if lumina.Mode != ModeAccount || work.Mode != ModeAccount || lumina.BoundAccountID != 101 || work.BoundAccountID != 202 {
		t.Fatalf("account bindings: lumina=%+v work=%+v", lumina, work)
	}
	if lumina.SessionPath == work.SessionPath || lumina.StateDir == work.StateDir || lumina.SessionPath == DefaultMainSessionPath {
		t.Fatal("account sessions or archives overlap")
	}
	if lumina.AppID != 77 || work.AppHash != "main-api-hash" || lumina.Phone != "" || work.Password != "" {
		t.Fatal("shared API credentials leaked user login credentials")
	}
	if err := BindAccountID("lumina22", 202); err == nil || !strings.Contains(err.Error(), "bound") {
		t.Fatalf("wrong-account binding error = %v", err)
	}
	accounts, err := ListAccounts()
	if err != nil || len(accounts) != 2 || accounts[0].Name != "lumina22" || accounts[1].Name != "work" {
		t.Fatalf("account list=%+v err=%v", accounts, err)
	}
	for _, name := range []string{"lumina22", "work"} {
		_, dir, err := ReadAccount(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{dir, filepath.Join(dir, "profile.json")} {
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("profile path %s is not private: %v %v", path, info, err)
			}
		}
	}
}

func TestAccountRegistrationRejectsUnsafeNamesAndDuplicates(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "accounts")
	for _, name := range []string{"../escape", "main", "study", "Bad", "a/b", "-dash", ""} {
		if _, _, err := createAccountAt(root, name, "main"); err == nil {
			t.Fatalf("accepted unsafe name %q", name)
		}
	}
	if _, _, err := createAccountAt(root, "work", "invalid"); err == nil {
		t.Fatal("accepted invalid API profile")
	}
	if _, _, err := createAccountAt(root, "work", "study"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := createAccountAt(root, "work", "main"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate registration error = %v", err)
	}
	if _, _, err := readAccountAt(root, "missing"); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unregistered account error = %v", err)
	}
}
