package mtproto

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/session/tdesktop"
	"github.com/gotd/td/telegram/auth"
)

func TestSelectDesktopAccountByTelegramID(t *testing.T) {
	accounts := []tdesktop.Account{
		{Authorization: tdesktop.MTPAuthorization{UserID: 111}},
		{Authorization: tdesktop.MTPAuthorization{UserID: 222}},
	}
	selected, err := selectDesktopAccount(accounts, 222)
	if err != nil || selected.Authorization.UserID != 222 {
		t.Fatalf("selected=%+v err=%v", selected.Authorization.UserID, err)
	}
	if _, err := selectDesktopAccount(accounts, 0); err == nil || !strings.Contains(err.Error(), "--desktop-user-id") {
		t.Fatalf("missing ID error=%v", err)
	}
	if _, err := selectDesktopAccount(accounts, 333); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("unknown ID error=%v", err)
	}
}

func TestCompleteDesktop2FARetriesRejectedPassword(t *testing.T) {
	attempts := 0
	err := completeDesktop2FA(context.Background(), func() (string, error) {
		attempts++
		return "password", nil
	}, func(_ context.Context, password string) error {
		if password != "password" {
			t.Fatalf("unexpected password %q", password)
		}
		if attempts == 1 {
			return auth.ErrPasswordInvalid
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestCompleteDesktop2FAStopsAfterThreeRejectedPasswords(t *testing.T) {
	attempts := 0
	err := completeDesktop2FA(context.Background(), func() (string, error) {
		attempts++
		return "wrong", nil
	}, func(context.Context, string) error { return auth.ErrPasswordInvalid })
	if !errors.Is(err, auth.ErrPasswordInvalid) || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}
