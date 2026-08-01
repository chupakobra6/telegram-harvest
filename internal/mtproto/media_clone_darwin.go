//go:build darwin

package mtproto

import "golang.org/x/sys/unix"

func cloneMediaFile(source, target string) error {
	return unix.Clonefile(source, target, 0)
}
