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

func TestInterviewTopicPublishCLIRejectsScopeBeforeSession(t *testing.T) {
	dir := t.TempDir()
	for _, scenario := range []string{"profile", "destination", "missing-manifest", "unknown-write", "positional", "public-manifest", "unknown-field"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := config.Config{Mode: config.ModeMain, SessionPath: filepath.Join(dir, "unused.session")}
			manifest := mtproto.InterviewTopicPublishManifest{Profile: "main", AccountID: mtproto.InterviewTopicAccountID, ChannelID: mtproto.InterviewTopicChannelID, TopicID: mtproto.InterviewTopicID,
				Posts: []mtproto.InterviewTopicPost{{Key: "screening", Text: "new"}}}
			if scenario == "profile" {
				cfg.Mode = config.ModeStudy
			}
			if scenario == "destination" {
				manifest.ChannelID++
			}
			path := filepath.Join(dir, "manifest.json")
			data, _ := json.Marshal(manifest)
			if scenario == "unknown-field" {
				data = []byte(`{"send":true}`)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			mode := os.FileMode(0o600)
			if scenario == "public-manifest" {
				mode = 0o644
			}
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			args := []string{"--manifest", path}
			switch scenario {
			case "missing-manifest":
				args = nil
			case "unknown-write":
				args = append(args, "--send")
			case "positional":
				args = append(args, "recipient")
			}
			if err := runInterviewTopicPublish(cfg, nil, args, &strings.Builder{}); err == nil {
				t.Fatal("invalid topic update entered live runtime")
			}
			if _, err := os.Stat(cfg.RuntimeLockPath()); !os.IsNotExist(err) {
				t.Fatal("rejected topic update acquired session resources", err)
			}
		})
	}
}
