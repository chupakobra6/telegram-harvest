//go:build !darwin

package mtproto

import "errors"

func cloneMediaFile(_, _ string) error {
	return errors.New("copy-on-write clone is unavailable")
}
