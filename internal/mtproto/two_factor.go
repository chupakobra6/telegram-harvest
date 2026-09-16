package mtproto

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/gotd/td/telegram/auth"
	"golang.org/x/term"
)

func completeTwoFactor(ctx context.Context, promptPassword func() (string, error), checkPassword func(context.Context, string) error) error {
	if promptPassword == nil {
		return fmt.Errorf("telegram 2FA password is required to complete login")
	}
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		password, err := promptPassword()
		if err != nil {
			return fmt.Errorf("read Telegram 2FA password: %w", err)
		}
		if err := checkPassword(ctx, password); err != nil {
			if !errors.Is(err, auth.ErrPasswordInvalid) {
				return fmt.Errorf("complete login with 2FA: %w", err)
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

func promptSecret(out *os.File, in *os.File, label string) (string, error) {
	if !term.IsTerminal(int(in.Fd())) {
		return "", fmt.Errorf("telegram 2FA password requires an interactive terminal")
	}
	_, _ = fmt.Fprint(out, label)
	password, err := term.ReadPassword(int(in.Fd()))
	_, _ = fmt.Fprintln(out)
	return string(password), err
}
