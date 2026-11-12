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
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/mtproto"
	"github.com/chupakobra6/telegram-harvest/internal/runlock"
)

type messageSession interface {
	PrepareMessage(context.Context, mtproto.MessagePrepareInput, mtproto.MessageAccount) (mtproto.MessageManifest, error)
	DeliverMessage(context.Context, mtproto.MessageManifest, mtproto.MessageAccount, string, bool) (mtproto.MessageResult, error)
	SaveMessageDraft(context.Context, mtproto.MessageManifest, mtproto.MessageAccount, bool) (mtproto.MessageResult, error)
}

type withMessageSession func(func(context.Context, messageSession) error) error

func messageAccount(cfg config.Config) (mtproto.MessageAccount, error) {
	switch cfg.Mode {
	case config.ModeMain:
		return mtproto.MessageAccount{Profile: "main", ID: mtproto.InterviewTopicAccountID, Username: mtproto.SavedMessagesMainUsername}, nil
	case config.ModeAccount:
		if cfg.BoundAccountID > 0 && cfg.AccountName != "" {
			return mtproto.MessageAccount{Profile: cfg.AccountName, ID: cfg.BoundAccountID}, nil
		}
		return mtproto.MessageAccount{}, fmt.Errorf("message requires a registered profile bound by login")
	default:
		return mtproto.MessageAccount{}, fmt.Errorf("message is unavailable for the read-only study profile")
	}
}

func runMessage(cfg config.Config, client *mtproto.Client, args []string, out, errOut io.Writer) error {
	return runMessageCommand(cfg, args, out, errOut, func(f func(context.Context, messageSession) error) error {
		return withRuntimeLock(cfg, func() error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return client.RunAuthorized(ctx, func(ctx context.Context, session *mtproto.Session) error {
				return f(ctx, session)
			})
		})
	})
}

func runMessageCommand(cfg config.Config, args []string, out, errOut io.Writer, withSession withMessageSession) error {
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		printMessageUsage(out)
		return nil
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: message prepare|show|send|draft [options]")
	}

	switch args[0] {
	case "prepare":
		return runMessagePrepare(cfg, args[1:], out, errOut, withSession)
	case "show", "send", "draft":
		return runMessageManifestCommand(cfg, args[0], args[1:], out, errOut, withSession)
	default:
		return fmt.Errorf("unknown message command %q; use message help", args[0])
	}
}

func printMessageUsage(out io.Writer) {
	fmt.Fprintln(out, "  message prepare --to user:ID|basic_group:ID|supergroup:ID|@username --manifest /absolute/private.json")
	fmt.Fprintln(out, "    [--text-file PATH] [--file PATH | --audio PATH | --video PATH] [--caption-file PATH] ... [--topic ID] [--reply-to ID] [--draft | --intent KEY] [--show]")
	fmt.Fprintln(out, "    Resolve recipient and save a private manifest; text then attachments in flag order, grouped by compatible kind. No Telegram write.")
	fmt.Fprintln(out, "  message show --manifest /absolute/private.json  # exact offline review, including text and attachments")
	fmt.Fprintln(out, "  message send --manifest /absolute/private.json [--apply]  # dry-run; apply only after explicit send authorization")
	fmt.Fprintln(out, "  message draft --manifest /absolute/private.json [--apply]  # dry-run; apply saves a native text draft; never sends")
	fmt.Fprintln(out, "  Use main or a bound registered profile. To prepare a native text draft, add --draft and omit attachments.")
	fmt.Fprintln(out, "  Use --intent KEY only to prepare a separate, deliberately repeated delivery. Reuse that manifest when checking its outcome.")
}

func messageJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func runMessagePrepare(cfg config.Config, args []string, out, errOut io.Writer, withSession withMessageSession) error {
	fs := flag.NewFlagSet("message prepare", flag.ContinueOnError)
	fs.SetOutput(errOut)
	path := fs.String("manifest", "", "absolute path to a new private review manifest")
	textPath := fs.String("text-file", "", "path to the exact UTF-8 text; all line breaks are preserved")
	show := fs.Bool("show", false, "print the full manifest with message text")
	var input mtproto.MessagePrepareInput
	fs.StringVar(&input.To, "to", "", "user:ID, basic_group:ID, supergroup:ID or @username")
	fs.IntVar(&input.TopicID, "topic", 0, "forum topic ID")
	fs.IntVar(&input.ReplyTo, "reply-to", 0, "message ID in this destination")
	fs.BoolVar(&input.Draft, "draft", false, "prepare a text-only native draft instead of a send")
	fs.Func("intent", "stable key for a deliberately repeated delivery", func(key string) error {
		if key == "" {
			return fmt.Errorf("intent key is empty")
		}
		input.Intent = key
		return nil
	})
	for _, kind := range []string{"file", "audio", "video"} {
		fs.Func(kind, "path to an attachment; repeat flags in delivery order", func(path string) error {
			if path == "" {
				return fmt.Errorf("attachment path is empty")
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			fileKind := kind
			if kind == "file" {
				fileKind = "document"
			}
			input.Files = append(input.Files, mtproto.MessageFileInput{Path: abs, Kind: fileKind})
			return nil
		})
	}
	fs.Func("caption-file", "UTF-8 caption for the preceding attachment", func(path string) error {
		if len(input.Files) == 0 || input.Files[len(input.Files)-1].Caption != "" {
			return fmt.Errorf("--caption-file must follow an attachment that does not yet have a caption")
		}
		caption, err := readMessageText(path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(caption) == "" {
			return fmt.Errorf("--caption-file must contain nonblank text")
		}
		input.Files[len(input.Files)-1].Caption = caption
		return nil
	})
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	account, err := messageAccount(cfg)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(*path) || input.To == "" || fs.NArg() != 0 {
		return fmt.Errorf("usage: message prepare --to <typed-id|@username> --manifest /absolute/private.json [options]")
	}
	if input.TopicID < 0 || input.ReplyTo < 0 {
		return fmt.Errorf("--topic and --reply-to must be positive IDs when supplied")
	}
	if input.Draft && len(input.Files) != 0 {
		return fmt.Errorf("native drafts support text only; omit attachment flags")
	}
	if input.Draft && input.Intent != "" {
		return fmt.Errorf("--intent is available for delivery only; omit it for a native draft")
	}
	if err := checkNewMessageManifest(*path); err != nil {
		return err
	}
	if *textPath != "" {
		input.Text, err = readMessageText(*textPath)
		if err != nil {
			return err
		}
		if strings.TrimSpace(input.Text) == "" {
			return fmt.Errorf("--text-file must contain nonblank text")
		}
	}
	if input.Text == "" && len(input.Files) == 0 {
		return fmt.Errorf("provide --text-file or an attachment")
	}

	var manifest mtproto.MessageManifest
	if err := withSession(func(ctx context.Context, session messageSession) error {
		var err error
		manifest, err = session.PrepareMessage(ctx, input, account)
		return err
	}); err != nil {
		return err
	}
	if err := mtproto.ValidateMessageManifest(manifest, account); err != nil {
		return err
	}
	if err := writeNewMessageManifest(*path, manifest); err != nil {
		return err
	}
	if *show {
		return messageJSON(out, manifest)
	}
	operation := "send"
	if manifest.Draft {
		operation = "draft"
	}
	return messageJSON(out, struct {
		Status      string                     `json:"status"`
		Manifest    string                     `json:"manifest"`
		Fingerprint string                     `json:"fingerprint"`
		Operation   string                     `json:"operation"`
		Intent      string                     `json:"intent,omitempty"`
		Account     mtproto.MessageAccount     `json:"account"`
		Destination mtproto.MessageDestination `json:"destination"`
		TopicID     int                        `json:"topic_id,omitempty"`
		ReplyTo     int                        `json:"reply_to,omitempty"`
		Attachments int                        `json:"attachments"`
	}{
		Status: "prepared", Manifest: *path, Fingerprint: manifest.Fingerprint,
		Operation: operation, Intent: manifest.Intent, Account: manifest.Account,
		Destination: manifest.Destination, TopicID: manifest.TopicID, ReplyTo: manifest.ReplyTo,
		Attachments: len(manifest.Files),
	})
}

func runMessageManifestCommand(cfg config.Config, operation string, args []string, out, errOut io.Writer, withSession withMessageSession) (err error) {
	fs := flag.NewFlagSet("message "+operation, flag.ContinueOnError)
	fs.SetOutput(errOut)
	path := fs.String("manifest", "", "absolute path to the private review manifest")
	apply := false
	switch operation {
	case "send":
		fs.BoolVar(&apply, "apply", false, "send exactly this reviewed message after explicit authorization")
	case "draft":
		fs.BoolVar(&apply, "apply", false, "save the explicitly requested native text draft")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	account, err := messageAccount(cfg)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(*path) || fs.NArg() != 0 {
		return fmt.Errorf("message %s requires --manifest /absolute/private.json", operation)
	}
	var manifest mtproto.MessageManifest
	if err := readMessageManifest(*path, &manifest); err != nil {
		return err
	}
	if err := mtproto.ValidateMessageManifest(manifest, account); err != nil {
		return err
	}
	if operation == "show" {
		return messageJSON(out, manifest)
	}
	if operation == "send" && manifest.Draft {
		return fmt.Errorf("this manifest is for a native draft; use message draft")
	}
	if operation == "draft" && !manifest.Draft {
		return fmt.Errorf("this manifest is for sending; prepare a native draft with --draft")
	}

	accountDir, err := messageAccountDir(account.ID)
	if err != nil {
		return err
	}
	if apply {
		lock, lockErr := lockMessageAccount(accountDir)
		if lockErr != nil {
			return lockErr
		}
		defer func() { err = errors.Join(err, lock.Release()) }()
	}
	return withSession(func(ctx context.Context, session messageSession) error {
		var result mtproto.MessageResult
		var runErr error
		if operation == "send" {
			result, runErr = session.DeliverMessage(ctx, manifest, account, accountDir, apply)
		} else {
			result, runErr = session.SaveMessageDraft(ctx, manifest, account, apply)
		}
		return errors.Join(runErr, messageJSON(out, result))
	})
}

func messageAccountDir(accountID int64) (string, error) {
	accounts, err := config.AccountStoreDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(accounts), "messages", fmt.Sprint(accountID)), nil
}

func lockMessageAccount(path string) (*runlock.Handle, error) {
	for _, dir := range []string{filepath.Dir(filepath.Dir(path)), filepath.Dir(path), path} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("message journal directory must be private (0700): %s", dir)
		}
	}
	return runlock.Acquire(filepath.Join(path, "mutation.lock"))
}

func readMessageText(path string) (string, error) {
	const maxTextBytes = 16 << 10
	f, err := openMessageInput(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxTextBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxTextBytes || !utf8.Valid(data) {
		return "", fmt.Errorf("message text must be valid UTF-8 of at most 16 KiB")
	}
	return string(data), nil
}

func openMessageInput(path string) (*os.File, error) {
	// Nonblocking open lets the subsequent regular-file check reject FIFOs
	// without hanging on their absent writer.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("message input must be a regular file: %s", path)
	}
	return f, nil
}

const maxMessageManifestBytes = 1 << 20

func readMessageManifest(path string, manifest *mtproto.MessageManifest) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&^0o600 != 0 || info.Size() > maxMessageManifestBytes {
		return fmt.Errorf("message manifest must be a private regular file (0600 or stricter), at most 1 MiB")
	}
	limited := &io.LimitedReader{R: f, N: maxMessageManifestBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(manifest); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("manifest must contain exactly one JSON object")
	}
	if limited.N == 0 {
		return fmt.Errorf("message manifest exceeds 1 MiB")
	}
	return nil
}

func checkNewMessageManifest(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("manifest already exists: %s: %w", path, os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("manifest parent must be an existing directory")
	}
	return nil
}

func writeNewMessageManifest(path string, manifest mtproto.MessageManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxMessageManifestBytes {
		return fmt.Errorf("message manifest exceeds 1 MiB")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".message-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// A same-directory link publishes only the completed file and fails if
	// another preparation claimed the name after our preflight.
	return os.Link(temp.Name(), path)
}
