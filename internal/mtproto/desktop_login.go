package mtproto

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/gotd/td/session"
	"github.com/gotd/td/session/tdesktop"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type DesktopAccount struct {
	Index    int
	UserID   int64
	Display  string
	Username string
}

func DefaultDesktopTDataPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "Telegram Desktop", "tdata"), nil
}

func ListDesktopAccounts(tdataPath string) ([]DesktopAccount, error) {
	accounts, err := readDesktopAccounts(tdataPath)
	if err != nil {
		return nil, err
	}
	result := make([]DesktopAccount, 0, len(accounts))
	for index, account := range accounts {
		if account.Authorization.UserID == 0 || account.Authorization.UserID > uint64(^uint64(0)>>1) {
			return nil, fmt.Errorf("desktop account %d has invalid Telegram ID", index)
		}
		result = append(result, DesktopAccount{Index: index, UserID: int64(account.Authorization.UserID)})
	}
	return result, nil
}

// IdentifyDesktopAccounts briefly connects each Desktop authorization to show
// which local account belongs to each Telegram ID. It does not persist keys.
func IdentifyDesktopAccounts(ctx context.Context, cfg config.Config, tdataPath string) ([]DesktopAccount, error) {
	if err := cfg.ValidateRuntime(); err != nil {
		return nil, err
	}
	accounts, err := readDesktopAccounts(tdataPath)
	if err != nil {
		return nil, err
	}
	result := make([]DesktopAccount, 0, len(accounts))
	for index, account := range accounts {
		client, err := borrowedDesktopClient(ctx, cfg, account)
		if err != nil {
			return nil, fmt.Errorf("prepare Desktop account %d: %w", index, err)
		}
		accountCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		var info DesktopAccount
		err = client.Run(accountCtx, func(runCtx context.Context) error {
			full, err := tg.NewClient(client).UsersGetFullUser(runCtx, &tg.InputUserSelf{})
			if err != nil {
				return err
			}
			info = DesktopAccount{Index: index, UserID: full.GetFullUser().ID}
			if info.UserID <= 0 || uint64(info.UserID) != account.Authorization.UserID {
				return fmt.Errorf("desktop account %d has a mismatched Telegram ID", index)
			}
			for _, userClass := range full.GetUsers() {
				user, ok := userClass.(*tg.User)
				if !ok || user.ID != info.UserID {
					continue
				}
				info.Display = strings.TrimSpace(user.FirstName + " " + user.LastName)
				info.Username, _ = user.GetUsername()
				break
			}
			return nil
		})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("identify Desktop account %d: %w", index, err)
		}
		result = append(result, info)
	}
	return result, nil
}

func readDesktopAccounts(tdataPath string) ([]tdesktop.Account, error) {
	if !filepath.IsAbs(tdataPath) {
		return nil, fmt.Errorf("telegram Desktop tdata path must be absolute")
	}
	accounts, err := tdesktop.Read(tdataPath, nil)
	if err != nil {
		return nil, fmt.Errorf("read Telegram Desktop tdata: %w", err)
	}
	if len(accounts) == 0 {
		return nil, fmt.Errorf("telegram Desktop tdata contains no accounts")
	}
	return accounts, nil
}

func selectDesktopAccount(accounts []tdesktop.Account, userID int64) (tdesktop.Account, error) {
	if userID <= 0 {
		return tdesktop.Account{}, fmt.Errorf("--desktop-user-id must be positive; use `account desktop-list` to find it")
	}
	for _, account := range accounts {
		if account.Authorization.UserID == uint64(userID) {
			return account, nil
		}
	}
	return tdesktop.Account{}, fmt.Errorf("telegram account ID %d is not present in Desktop tdata", userID)
}

// LoginFromDesktop uses a Desktop authorization only long enough to approve a
// new, independent MTProto authorization. It never writes to tdata or persists
// the borrowed Desktop key in the Harvest session.
func (c *Client) LoginFromDesktop(ctx context.Context, tdataPath string, userID int64, promptPassword func() (string, error)) error {
	if c.cfg.Mode != config.ModeAccount {
		return fmt.Errorf("desktop login is supported only for registered full-account profiles")
	}
	if err := c.cfg.ValidateRuntime(); err != nil {
		return err
	}
	if c.cfg.BoundAccountID != 0 && c.cfg.BoundAccountID != userID {
		return fmt.Errorf("profile %q is bound to Telegram ID %d", c.cfg.AccountName, c.cfg.BoundAccountID)
	}
	accounts, err := readDesktopAccounts(tdataPath)
	if err != nil {
		return err
	}
	account, err := selectDesktopAccount(accounts, userID)
	if err != nil {
		return err
	}
	borrowed, err := borrowedDesktopClient(ctx, c.cfg, account)
	if err != nil {
		return err
	}
	if err := ensureSessionDir(c.cfg.SessionPath); err != nil {
		return err
	}
	if _, err := os.Stat(c.cfg.SessionPath); err == nil {
		status, err := c.AuthStatus(ctx)
		if err != nil {
			return fmt.Errorf("check existing Harvest session before Desktop login: %w", err)
		}
		if status.Authorized {
			return fmt.Errorf("profile %q already has an authorized Harvest session", c.cfg.AccountName)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	tempDir, err := os.MkdirTemp(filepath.Dir(c.cfg.SessionPath), ".desktop-login-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	newSessionPath := filepath.Join(tempDir, "session.json")
	fresh := telegram.NewClient(c.cfg.AppID, c.cfg.AppHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: newSessionPath},
		Resolver:       dcs.Plain(dcs.PlainOptions{Dial: proxyAwareDialContext}),
	})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	err = fresh.Run(ctx, func(freshCtx context.Context) error {
		token, err := fresh.QR().Export(freshCtx)
		if err != nil {
			return fmt.Errorf("create independent login token: %w", err)
		}
		if token.Empty() {
			return fmt.Errorf("independent login token is empty")
		}
		if err := borrowed.Run(freshCtx, func(borrowedCtx context.Context) error {
			currentID, err := selfID(borrowedCtx, borrowed)
			if err != nil {
				return fmt.Errorf("verify Desktop authorization: %w", err)
			}
			if currentID != userID {
				return fmt.Errorf("desktop authorization ID %d does not match selected ID %d", currentID, userID)
			}
			if _, err := borrowed.QR().Accept(borrowedCtx, token); err != nil {
				return fmt.Errorf("approve login from Desktop authorization: %w", err)
			}
			return nil
		}); err != nil {
			return err
		}
		if _, err := fresh.QR().Import(freshCtx); err != nil {
			if !tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
				return fmt.Errorf("complete independent login: %w", err)
			}
			if err := completeDesktop2FA(freshCtx, promptPassword, func(ctx context.Context, password string) error {
				_, err := fresh.Auth().Password(ctx, password)
				return err
			}); err != nil {
				return err
			}
		}
		newID, err := selfID(freshCtx, fresh)
		if err != nil {
			return fmt.Errorf("verify independent authorization: %w", err)
		}
		if newID != userID {
			return fmt.Errorf("independent authorization ID %d does not match selected ID %d", newID, userID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := config.BindAccountID(c.cfg.AccountName, userID); err != nil {
		return err
	}
	if err := os.Rename(newSessionPath, c.cfg.SessionPath); err != nil {
		return fmt.Errorf("publish independent Harvest session: %w", err)
	}
	return nil
}

func completeDesktop2FA(ctx context.Context, promptPassword func() (string, error), checkPassword func(context.Context, string) error) error {
	if promptPassword == nil {
		return fmt.Errorf("telegram 2FA password is required to complete independent login")
	}
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		password, err := promptPassword()
		if err != nil {
			return fmt.Errorf("read Telegram 2FA password: %w", err)
		}
		if err := checkPassword(ctx, password); err != nil {
			if !errors.Is(err, auth.ErrPasswordInvalid) {
				return fmt.Errorf("complete independent login with 2FA: %w", err)
			}
			if attempt == maxAttempts {
				return fmt.Errorf("telegram rejected the 2FA password after %d attempts: %w", maxAttempts, err)
			}
			continue
		}
		return nil
	}
	return nil
}

func borrowedDesktopClient(ctx context.Context, cfg config.Config, account tdesktop.Account) (*telegram.Client, error) {
	data, err := session.TDesktopSession(account)
	if err != nil {
		return nil, fmt.Errorf("decode Desktop authorization: %w", err)
	}
	storage := &session.StorageMemory{}
	loader := session.Loader{Storage: storage}
	if err := loader.Save(ctx, data); err != nil {
		return nil, fmt.Errorf("load Desktop authorization in memory: %w", err)
	}
	return telegram.NewClient(cfg.AppID, cfg.AppHash, telegram.Options{
		SessionStorage: storage,
		Resolver:       dcs.Plain(dcs.PlainOptions{Dial: proxyAwareDialContext}),
	}), nil
}

func selfID(ctx context.Context, client *telegram.Client) (int64, error) {
	result, err := tg.NewClient(client).UsersGetFullUser(ctx, &tg.InputUserSelf{})
	if err != nil {
		return 0, err
	}
	return result.GetFullUser().ID, nil
}
