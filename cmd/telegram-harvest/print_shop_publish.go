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

func runPrintShopPublish(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	if cfg.Mode != config.ModeMain {
		return fmt.Errorf("print-shop-publish is supported only for main/@Pheik13")
	}
	fs := flag.NewFlagSet("print-shop-publish", flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("manifest", "", "private reviewed manifest for the fixed print-shop group")
	apply := fs.Bool("apply", false, "publish reviewed posts in fixed print-shop group; no generic recipient")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: print-shop-publish --manifest <private.json> [--apply]")
	}
	var manifest mtproto.PrintShopPublishManifest
	err := readPrivatePublishManifest(*path, &manifest)
	if err != nil {
		return err
	}
	if err := mtproto.ValidatePrintShopPublishManifest(manifest); err != nil {
		return err
	}
	return withRuntimeLock(cfg, func() error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return client.RunAuthorized(ctx, func(ctx context.Context, session *mtproto.Session) error {
			result, runErr := session.PublishPrintShop(ctx, manifest, *path+".receipts.json", *apply)
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			return errors.Join(runErr, encoder.Encode(result))
		})
	})
}
