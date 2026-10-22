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

func accountOrganizationConfig(cfg config.Config) error {
	if cfg.Mode != config.ModeAccount || cfg.BoundAccountID <= 0 {
		return fmt.Errorf("account folder commands require a registered, authorized full-account profile")
	}
	return nil
}

func runAccountFolders(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	if err := accountOrganizationConfig(cfg); err != nil {
		return err
	}
	if len(args) != 0 {
		return fmt.Errorf("account-folders accepts no arguments")
	}
	return withRuntimeLock(cfg, func() error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return client.RunAuthorized(ctx, func(ctx context.Context, session *mtproto.Session) error {
			result, err := session.AccountFolders(ctx, cfg.BoundAccountID)
			if err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(result)
		})
	})
}

func readAccountOrganizeManifest(path string) (mtproto.AccountOrganizeManifest, error) {
	var manifest mtproto.AccountOrganizeManifest
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

func runAccountOrganize(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	if err := accountOrganizationConfig(cfg); err != nil {
		return err
	}
	fs := flag.NewFlagSet("account-organize", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("manifest", "", "private reviewed account/user/head manifest")
	apply := fs.Bool("apply", false, "apply only Отработка folder membership and archive metadata")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: account-organize --manifest <private.json> [--apply]")
	}
	manifest, err := readAccountOrganizeManifest(*path)
	if err != nil {
		return err
	}
	if err := mtproto.ValidateAccountOrganizeManifest(manifest, cfg.ProfileName(), cfg.BoundAccountID); err != nil {
		return err
	}
	return withRuntimeLock(cfg, func() error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return client.RunAuthorized(ctx, func(ctx context.Context, session *mtproto.Session) error {
			result, runErr := session.OrganizeAccount(ctx, manifest, *apply)
			// Keep partial verified rows visible even when a later peer fails.
			return errors.Join(runErr, json.NewEncoder(out).Encode(result))
		})
	})
}
