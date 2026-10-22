package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/mtproto"
)

func TestAccountOrganizationRejectsWrongModeAndManifestBeforeRuntime(t *testing.T) {
	dir := t.TempDir()
	manifest := mtproto.AccountOrganizeManifest{Profile: "work", AccountID: 99, FolderName: mtproto.CompletedWorkFolder, Peers: []mtproto.AccountOrganizePeer{{UserID: 11, LastMessageID: 10}}}
	path := filepath.Join(dir, "review.json")
	encoded, _ := json.Marshal(manifest)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"main", "study", "unbound", "profile", "account", "unknown-flag", "positional", "no-manifest"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := config.Config{Mode: config.ModeAccount, AccountName: "work", BoundAccountID: 99, SessionPath: filepath.Join(dir, "unused.session")}
			args := []string{"--manifest", path}
			switch scenario {
			case "main":
				cfg.Mode = config.ModeMain
			case "study":
				cfg.Mode = config.ModeStudy
			case "unbound":
				cfg.BoundAccountID = 0
			case "profile":
				cfg.AccountName = "other"
			case "account":
				cfg.BoundAccountID++
			case "unknown-flag":
				args = append(args, "--send")
			case "positional":
				args = append(args, "recipient")
			case "no-manifest":
				args = nil
			}
			var output strings.Builder
			// A nil client makes any accidental runtime entry fail this test.
			if err := runAccountOrganize(cfg, nil, args, &output); err == nil {
				t.Fatal("invalid organization input reached runtime")
			}
			if _, err := os.Stat(cfg.RuntimeLockPath()); !os.IsNotExist(err) {
				t.Fatal("invalid organization input acquired session resources", err)
			}
		})
	}
}

func TestAccountOrganizeManifestRequiresPrivateStrictJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	for _, scenario := range []struct {
		name string
		body string
		mode os.FileMode
	}{
		{"public", `{}`, 0o644},
		{"unknown", `{"send":true}`, 0o600},
		{"trailing", `{} {}`, 0o600},
		{"malformed", `{"profile":`, 0o600},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(scenario.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, scenario.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := readAccountOrganizeManifest(path); err == nil {
				t.Fatal("unsafe manifest file accepted", scenario.name)
			}
		})
	}
}

func TestAccountFoldersRejectsUnboundProfilesAndArguments(t *testing.T) {
	for _, cfg := range []config.Config{{Mode: config.ModeMain}, {Mode: config.ModeStudy}, {Mode: config.ModeAccount}} {
		if err := runAccountFolders(cfg, nil, nil, &strings.Builder{}); err == nil {
			t.Fatal("unbound folder read accepted")
		}
	}
	cfg := config.Config{Mode: config.ModeAccount, AccountName: "work", BoundAccountID: 99}
	if err := runAccountFolders(cfg, nil, []string{"--apply"}, &strings.Builder{}); err == nil {
		t.Fatal("folder listing accepted a mutation option")
	}
}
