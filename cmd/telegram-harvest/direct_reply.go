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
	"path/filepath"
	"syscall"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/mtproto"
	"github.com/chupakobra6/telegram-harvest/internal/runlock"
)

func runDirectReply(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	if cfg.Mode != config.ModeMain {
		return fmt.Errorf("direct-reply is supported only for main/@Pheik13")
	}
	fs := flag.NewFlagSet("direct-reply", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("manifest", "", "absolute path to private reviewed reply manifest")
	apply := fs.Bool("apply", false, "send the explicitly authorized reply")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !filepath.IsAbs(*path) || fs.NArg() != 0 {
		return fmt.Errorf("usage: direct-reply --manifest /absolute/private.json [--apply]")
	}
	var manifest mtproto.DirectReplyManifest
	if err := readPrivatePublishManifest(*path, &manifest); err != nil {
		return err
	}
	if err := mtproto.ValidateDirectReplyManifest(manifest); err != nil {
		return err
	}
	accountsDir, err := config.AccountStoreDir()
	if err != nil {
		return err
	}
	journal := filepath.Join(filepath.Dir(accountsDir), "direct-reply", "main.receipts.json")
	return withRuntimeLock(cfg, func() (err error) {
		if *apply {
			if err := os.MkdirAll(filepath.Dir(journal), 0o700); err != nil {
				return err
			}
			lock, err := runlock.AcquireResources(journal)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, lock.Release()) }()
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return client.RunAuthorized(ctx, func(ctx context.Context, session *mtproto.Session) error {
			result, runErr := session.SendDirectReply(ctx, manifest, journal, *apply)
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			return errors.Join(runErr, encoder.Encode(result))
		})
	})
}
