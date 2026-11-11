package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/mtproto"
	"github.com/chupakobra6/telegram-harvest/internal/runlock"
)

type fakeMessageSession struct {
	prepare func(context.Context, mtproto.MessagePrepareInput, mtproto.MessageAccount) (mtproto.MessageManifest, error)
	deliver func(context.Context, mtproto.MessageManifest, mtproto.MessageAccount, string, bool) (mtproto.MessageResult, error)
	draft   func(context.Context, mtproto.MessageManifest, mtproto.MessageAccount, bool) (mtproto.MessageResult, error)
}

func (s fakeMessageSession) PrepareMessage(ctx context.Context, input mtproto.MessagePrepareInput, account mtproto.MessageAccount) (mtproto.MessageManifest, error) {
	return s.prepare(ctx, input, account)
}

func (s fakeMessageSession) DeliverMessage(ctx context.Context, manifest mtproto.MessageManifest, account mtproto.MessageAccount, dir string, apply bool) (mtproto.MessageResult, error) {
	return s.deliver(ctx, manifest, account, dir, apply)
}

func (s fakeMessageSession) SaveMessageDraft(ctx context.Context, manifest mtproto.MessageManifest, account mtproto.MessageAccount, apply bool) (mtproto.MessageResult, error) {
	return s.draft(ctx, manifest, account, apply)
}

func testMessageManifest(t *testing.T, account mtproto.MessageAccount, exactText string, draft bool) mtproto.MessageManifest {
	t.Helper()
	m := mtproto.MessageManifest{
		Account:     account,
		Destination: mtproto.MessageDestination{Type: "user", ID: 42, Username: "recipient", Title: "Recipient", Selector: "@recipient"},
		Text:        exactText,
		Draft:       draft,
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(data)
	m.Fingerprint = hex.EncodeToString(fingerprint[:])
	return m
}

func TestMessagePrepareWritesPrivateReviewWithoutDispatch(t *testing.T) {
	cfg := config.Config{Mode: config.ModeMain}
	root := t.TempDir()
	source := filepath.Join(root, "message text.txt")
	manifestPath := filepath.Join(root, "review.json")
	wantText := "  Привет, @recipient!\n\n<текст> 🙂  \n"
	mustWriteCLIFile(t, source, wantText)
	var output, diagnostics strings.Builder
	withSession := func(f func(context.Context, messageSession) error) error {
		return f(t.Context(), fakeMessageSession{prepare: func(_ context.Context, input mtproto.MessagePrepareInput, account mtproto.MessageAccount) (mtproto.MessageManifest, error) {
			if input.To != "@recipient" || input.Text != wantText || input.Draft || account.Profile != "main" || account.Username != mtproto.SavedMessagesMainUsername {
				t.Fatalf("unexpected prepare input: %+v, account: %+v", input, account)
			}
			return testMessageManifest(t, account, input.Text, input.Draft), nil
		}})
	}
	args := []string{"prepare", "--to", "@recipient", "--manifest", manifestPath, "--text-file", source}
	if err := runMessageCommand(cfg, args, &output, &diagnostics, withSession); err != nil {
		t.Fatalf("prepare: %v (%s)", err, &diagnostics)
	}
	if strings.Contains(output.String(), "Привет") || !strings.Contains(output.String(), `"status": "prepared"`) {
		t.Fatalf("expected compact preparation receipt, got %s", &output)
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode = %o", info.Mode().Perm())
	}
	offline := func(func(context.Context, messageSession) error) error {
		t.Fatal("offline operation attempted to open a Telegram session")
		return nil
	}
	output.Reset()
	if err := runMessageCommand(cfg, []string{"show", "--manifest", manifestPath}, &output, &diagnostics, offline); err != nil {
		t.Fatal(err)
	}
	var m mtproto.MessageManifest
	if err := json.Unmarshal([]byte(output.String()), &m); err != nil || m.Text != wantText || m.Destination.ID != 42 {
		t.Fatalf("invalid exact review: %+v err=%v", m, err)
	}
	if err := runMessageCommand(cfg, args, io.Discard, &diagnostics, offline); !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate preparation error = %v", err)
	}
	if err := writeNewMessageManifest(manifestPath, mtproto.MessageManifest{}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("atomic publication did not reject an occupied path: %v", err)
	}
	var original mtproto.MessageManifest
	if err := readMessageManifest(manifestPath, &original); err != nil || original.Fingerprint != m.Fingerprint {
		t.Fatalf("occupied review changed: %+v err=%v", original, err)
	}
}

func TestMessagePrepareKeepsMixedAttachmentOrder(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{Mode: config.ModeMain}
	doc, audio, video, last := filepath.Join(root, "report.docx"), filepath.Join(root, "sound.mp3"), filepath.Join(root, "film.mp4"), filepath.Join(root, "notes.zip")
	args := []string{"prepare", "--to", "supergroup:42", "--manifest", filepath.Join(root, "review.json"),
		"--file", doc, "--audio", audio, "--video", video, "--file", last, "--topic", "10", "--reply-to", "11", "--intent", "second-delivery"}
	want := []mtproto.MessageFileInput{{Path: doc, Kind: "document"}, {Path: audio, Kind: "audio"}, {Path: video, Kind: "video"}, {Path: last, Kind: "document"}}
	finished := errors.New("prepared input captured")
	withSession := func(f func(context.Context, messageSession) error) error {
		return f(t.Context(), fakeMessageSession{prepare: func(_ context.Context, input mtproto.MessagePrepareInput, _ mtproto.MessageAccount) (mtproto.MessageManifest, error) {
			if !reflect.DeepEqual(input.Files, want) || input.TopicID != 10 || input.ReplyTo != 11 || input.Intent != "second-delivery" {
				t.Fatalf("unexpected input: %+v", input)
			}
			return mtproto.MessageManifest{}, finished
		}})
	}
	if err := runMessageCommand(cfg, args, io.Discard, io.Discard, withSession); !errors.Is(err, finished) {
		t.Fatalf("prepare err=%v", err)
	}
}

func TestMessageRejectsUnsafeInputsBeforeTelegram(t *testing.T) {
	cfg := config.Config{Mode: config.ModeMain}
	dir := t.TempDir()
	path := filepath.Join(dir, "review.json")
	account, err := messageAccount(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNewMessageManifest(path, testMessageManifest(t, account, "test", false)); err != nil {
		t.Fatal(err)
	}
	offline := func(func(context.Context, messageSession) error) error {
		t.Fatal("invalid input attempted to open a Telegram session")
		return nil
	}
	manifestArgs := []string{"--manifest", path}
	for _, tc := range []struct {
		name string
		cfg  config.Config
		args []string
		want string
	}{
		{"unknown flag", cfg, []string{"send", "--to", "@other", "--manifest", path}, "flag provided but not defined"},
		{"prepare cannot apply", cfg, []string{"prepare", "--apply"}, "flag provided but not defined"},
		{"empty intent", cfg, []string{"prepare", "--intent="}, "intent key is empty"},
		{"study", config.Config{Mode: config.ModeStudy}, append([]string{"send", "--apply"}, manifestArgs...), "read-only study"},
		{"wrong account", config.Config{Mode: config.ModeAccount, AccountName: "work", BoundAccountID: 500}, append([]string{"send", "--apply"}, manifestArgs...), "manifest account differs"},
		{"wrong profile for same ID", config.Config{Mode: config.ModeAccount, AccountName: "work", BoundAccountID: account.ID}, append([]string{"send", "--apply"}, manifestArgs...), "manifest account differs"},
		{"unbound", config.Config{Mode: config.ModeAccount, AccountName: "work"}, append([]string{"send", "--apply"}, manifestArgs...), "bound by login"},
		{"send manifest cannot make a draft", cfg, append([]string{"draft", "--apply"}, manifestArgs...), "manifest is for sending"},
		{"native draft with files", cfg, []string{"prepare", "--draft", "--to", "@recipient", "--manifest", filepath.Join(dir, "new.json"), "--file", "test.pdf"}, "native drafts support text only"},
		{"intent only for delivery", cfg, []string{"prepare", "--draft", "--to", "@recipient", "--manifest", filepath.Join(dir, "new.json"), "--intent", "second-delivery"}, "--intent is available for delivery only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := runMessageCommand(tc.cfg, tc.args, io.Discard, io.Discard, offline); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestRunMessageRoutesWithoutConnectingToTelegram(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.json")
	account, err := messageAccount(config.Config{Mode: config.ModeMain})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeNewMessageManifest(path, testMessageManifest(t, account, "exact offline text", false)); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCommand(t, []string{"--profile", "main", "message", "show", "--manifest", path}, nil)
	if code != 0 || !strings.Contains(stdout, "exact offline text") || stderr != "" {
		t.Fatalf("offline show: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runCommand(t, []string{"--profile", "study", "message", "send", "--manifest", path, "--apply"}, nil)
	if code == 0 || stdout != "" || !strings.Contains(stderr, "read-only study") {
		t.Fatalf("study apply: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestMessageSendDryRunAndAccountMutationLock(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	cfg := config.Config{Mode: config.ModeMain}
	account, err := messageAccount(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "review.json")
	if err := writeNewMessageManifest(path, testMessageManifest(t, account, "test", false)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var output strings.Builder
	withSession := func(f func(context.Context, messageSession) error) error {
		return f(t.Context(), fakeMessageSession{deliver: func(_ context.Context, m mtproto.MessageManifest, a mtproto.MessageAccount, dir string, apply bool) (mtproto.MessageResult, error) {
			calls++
			if m.Text != "test" || a.ID != account.ID {
				t.Fatalf("unexpected manifest or account: %+v %+v", m, a)
			}
			if apply {
				reentry, lockErr := lockMessageAccount(dir)
				if reentry != nil {
					_ = reentry.Release()
				}
				if !errors.Is(lockErr, runlock.ErrAlreadyLocked) {
					t.Fatalf("missing account mutation lock: %v", lockErr)
				}
			} else if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("dry-run created a journal directory: %v", err)
			}
			return mtproto.MessageResult{DryRun: !apply, Ready: true, Status: "ready"}, nil
		}})
	}
	args := []string{"send", "--manifest", path}
	if err := runMessageCommand(cfg, args, &output, io.Discard, withSession); err != nil || !strings.Contains(output.String(), `"dry_run": true`) {
		t.Fatalf("default dry-run: err=%v output=%s", err, &output)
	}
	output.Reset()
	if err := runMessageCommand(cfg, append(args, "--apply"), &output, io.Discard, withSession); err != nil || !strings.Contains(output.String(), `"dry_run": false`) {
		t.Fatalf("explicit apply: err=%v output=%s", err, &output)
	}
	if calls != 2 {
		t.Fatalf("send path calls=%d", calls)
	}
}

func TestMessageDraftApplyUsesOnlyDraftOperation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	cfg := config.Config{Mode: config.ModeMain}
	account, err := messageAccount(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "draft.json")
	if err := writeNewMessageManifest(path, testMessageManifest(t, account, "draft only", true)); err != nil {
		t.Fatal(err)
	}
	withSession := func(f func(context.Context, messageSession) error) error {
		return f(t.Context(), fakeMessageSession{draft: func(_ context.Context, m mtproto.MessageManifest, _ mtproto.MessageAccount, apply bool) (mtproto.MessageResult, error) {
			if !apply || !m.Draft || m.Text != "draft only" {
				t.Fatalf("unexpected draft request: %+v apply=%t", m, apply)
			}
			return mtproto.MessageResult{Complete: true, Status: "draft_verified"}, nil
		}})
	}
	var output strings.Builder
	if err := runMessageCommand(cfg, []string{"draft", "--manifest", path, "--apply"}, &output, io.Discard, withSession); err != nil || !strings.Contains(output.String(), `"status": "draft_verified"`) {
		t.Fatalf("draft apply: err=%v output=%s", err, &output)
	}
	if err := runMessageCommand(cfg, []string{"send", "--manifest", path, "--apply"}, io.Discard, io.Discard, withSession); err == nil || !strings.Contains(err.Error(), "manifest is for a native draft") {
		t.Fatalf("send of draft manifest: err=%v", err)
	}
}
