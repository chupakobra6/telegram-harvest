package mtproto

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/telegram/auth"
)

func TestCompleteTwoFactorRetriesRejectedPassword(t *testing.T) {
	attempts := 0
	err := completeTwoFactor(context.Background(), func() (string, error) {
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

func TestCompleteTwoFactorStopsAfterThreeRejectedPasswords(t *testing.T) {
	attempts := 0
	err := completeTwoFactor(context.Background(), func() (string, error) {
		attempts++
		return "wrong", nil
	}, func(context.Context, string) error { return auth.ErrPasswordInvalid })
	if !errors.Is(err, auth.ErrPasswordInvalid) || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}
