package mtproto

import (
	"strings"
	"testing"

	"github.com/gotd/td/session/tdesktop"
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
