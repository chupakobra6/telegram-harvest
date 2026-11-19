package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-harvest/internal/mtproto"
)

func runReadPost(cfg config.Config, client *mtproto.Client, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("read-post", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprintln(out, "usage: telegram-harvest --profile <name> read-post [--json] <t.me-message-url>")
		fs.PrintDefaults()
	}
	jsonOut := fs.Bool("json", false, "print one normalized Telegram message as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("read-post takes exactly one t.me message URL")
	}
	link, err := mtproto.ParsePostLink(fs.Arg(0))
	if err != nil {
		return err
	}
	selector := link.ChatSelector()
	if !postLinkMayMatchAllowlist(cfg, selector) {
		return ensureAllowedChat(cfg, selector)
	}
	if cfg.Mode == config.ModeAccount && cfg.BoundAccountID <= 0 {
		return fmt.Errorf("account %q is not authorized; run `%s` first", cfg.AccountName, cfg.LoginCommand())
	}
	var result mtproto.ReadPostResult
	if err := withReadRuntimeLock(cfg, func() error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return client.RunAuthorized(ctx, func(ctx context.Context, session *mtproto.Session) error {
			if cfg.Mode == config.ModeAccount {
				self, err := session.SelfProfile(ctx)
				if err != nil {
					return fmt.Errorf("identify authorized account: %w", err)
				}
				if self.ID != cfg.BoundAccountID {
					return fmt.Errorf("authorized Telegram session differs from the registered account")
				}
			}
			var err error
			result, err = session.ReadPost(ctx, link, func(chat harvest.Chat) error {
				return ensureResolvedPostChatAllowed(cfg, selector, chat)
			})
			return err
		})
	}); err != nil {
		return err
	}
	if *jsonOut {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	return writeReadPostText(out, result)
}

func writeReadPostText(out io.Writer, result mtproto.ReadPostResult) error {
	var report strings.Builder
	record := result.Record
	fmt.Fprintf(&report, "%s\nchat=%q", record.SourceURL, record.Chat.Display)
	if record.Chat.Username != "" {
		fmt.Fprintf(&report, " @%s", record.Chat.Username)
	}
	fmt.Fprintf(&report, "\nchat_id=%d message_id=%d date=%s kind=%s scope=%s attachments=%d grouped_media=%t media_downloaded=%t\n",
		record.Chat.ID, record.MessageID, record.Date.Format(time.RFC3339),
		record.Kind, result.Scope, len(record.Attachments), result.GroupedMedia, result.MediaDownloaded)
	if record.Text != "" {
		fmt.Fprintf(&report, "\n%s\n", record.Text)
	}
	_, err := io.WriteString(out, report.String())
	return err
}

// An opposite selector form may match once Telegram resolves the chat. Refuse
// definite mismatches before acquiring the session or doing any RPC.
func postLinkMayMatchAllowlist(cfg config.Config, selector string) bool {
	if cfg.ChatAllowed(selector) {
		return true
	}
	publicLink := strings.HasPrefix(selector, "@")
	for _, allowed := range cfg.AllowedChats {
		allowed = strings.TrimPrefix(strings.TrimSpace(allowed), "@")
		if kind, id, typed := strings.Cut(allowed, ":"); typed {
			if kind != "channel" && kind != "supergroup" {
				continue
			}
			allowed = id
		}
		id, err := strconv.ParseInt(allowed, 10, 64)
		if publicLink && err == nil && id > 0 {
			return true
		}
		if !publicLink && (err != nil || selector == allowed) {
			return true
		}
	}
	return false
}

func ensureResolvedPostChatAllowed(cfg config.Config, selector string, chat harvest.Chat) error {
	if cfg.ChatAllowed(strconv.FormatInt(chat.ID, 10)) || cfg.ChatAllowed(harvest.AccountChatKey(chat)) ||
		(chat.Username != "" && cfg.ChatAllowed(chat.Username)) {
		return nil
	}
	// A public link can address an active alias besides the primary username.
	return ensureAllowedChat(cfg, selector)
}
