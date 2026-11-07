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

func TestPrintShopPublishCLIRejectsScopeBeforeSession(t *testing.T) {
	dir := t.TempDir()
	for _, scenario := range []string{"profile", "destination", "missing-manifest", "unknown-write", "positional", "public-manifest", "unknown-field", "recipient", "topic", "reply", "file-kind", "extra-json"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := config.Config{Mode: config.ModeMain, SessionPath: filepath.Join(dir, "unused.session")}
			manifest := mtproto.PrintShopPublishManifest{Profile: "main", AccountID: mtproto.InterviewTopicAccountID, ChannelID: mtproto.PrintShopChannelID,
				Posts: []mtproto.PrintShopPost{{Key: "screening", Text: "new"}}}
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
			if scenario == "recipient" || scenario == "topic" || scenario == "reply" || scenario == "file-kind" || scenario == "extra-json" {
				switch scenario {
				case "recipient":
					data = []byte(`{"profile":"main","account_id":1266978055,"channel_id":4415343946,"recipient":"user:1221157785","posts":[{"key":"a","text":"x"}]}`)
				case "topic":
					data = []byte(`{"profile":"main","account_id":1266978055,"channel_id":4415343946,"topic_id":115,"posts":[{"key":"a","text":"x"}]}`)
				case "reply":
					data = []byte(`{"profile":"main","account_id":1266978055,"channel_id":4415343946,"posts":[{"key":"a","text":"x","parent_id":1}]}`)
				case "file-kind":
					data = []byte(`{"profile":"main","account_id":1266978055,"channel_id":4415343946,"posts":[{"key":"a","text":"x","file":{"kind":"video"}}]}`)
				case "extra-json":
					data = append(data, []byte(` {}`)...)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
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
			if err := runPrintShopPublish(cfg, nil, args, &strings.Builder{}); err == nil {
				t.Fatal("invalid print-shop publish entered live runtime")
			}
			if _, err := os.Stat(cfg.RuntimeLockPath()); !os.IsNotExist(err) {
				t.Fatal("rejected print-shop publish acquired session resources", err)
			}
		})
	}
}
