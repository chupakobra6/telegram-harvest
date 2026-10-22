package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/mtproto"
)

func readInterviewTopicManifest(path string) (mtproto.InterviewTopicUpdateManifest, error) {
	var manifest mtproto.InterviewTopicUpdateManifest
	f, err := os.Open(path)
	if err != nil {
		return manifest, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return manifest, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 1<<20 {
		return manifest, fmt.Errorf("manifest must be a private regular file (0600 or stricter), at most 1 MiB")
	}
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return manifest, fmt.Errorf("manifest must contain exactly one JSON object")
	}
	return manifest, nil
}

func runInterviewTopicUpdate(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	if cfg.Mode != config.ModeMain {
		return fmt.Errorf("interview-topic-update is supported only for main/@Pheik13")
	}
	fs := flag.NewFlagSet("interview-topic-update", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("manifest", "", "private reviewed manifest for the fixed interview topic")
	apply := fs.Bool("apply", false, "apply own edits and the two explicitly backed-up ZIP removals")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: interview-topic-update --manifest <private.json> [--apply]")
	}
	manifest, err := readInterviewTopicManifest(*path)
	if err != nil {
		return err
	}
	if err := mtproto.ValidateInterviewTopicUpdateManifest(manifest); err != nil {
		return err
	}
	return withRuntimeLock(cfg, func() error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return client.RunAuthorized(ctx, func(ctx context.Context, session *mtproto.Session) error {
			result, runErr := session.UpdateInterviewTopic(ctx, manifest, *apply)
			return errors.Join(runErr, json.NewEncoder(out).Encode(result))
		})
	})
}
