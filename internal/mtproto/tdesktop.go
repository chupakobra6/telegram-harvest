package mtproto

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gotd/td/session"
	"github.com/gotd/td/session/tdesktop"
)

type TDesktopImportResult struct {
	AccountCount int    `json:"account_count"`
	AccountIndex int    `json:"account_index"`
	SessionPath  string `json:"session_path"`
	DC           int    `json:"dc"`
	Addr         string `json:"addr"`
}

func ImportTDesktopSession(ctx context.Context, tdataPath string, accountIndex int, sessionPath string, passcode []byte) (TDesktopImportResult, error) {
	if tdataPath == "" {
		return TDesktopImportResult{}, fmt.Errorf("tdata path is required")
	}
	if sessionPath == "" {
		return TDesktopImportResult{}, fmt.Errorf("session path is required")
	}
	accounts, err := tdesktop.Read(tdataPath, passcode)
	if err != nil {
		return TDesktopImportResult{}, fmt.Errorf("read Telegram Desktop tdata: %w", err)
	}
	if len(accounts) == 0 {
		return TDesktopImportResult{}, fmt.Errorf("no Telegram Desktop accounts found in %s", tdataPath)
	}
	if accountIndex < 0 || accountIndex >= len(accounts) {
		return TDesktopImportResult{}, fmt.Errorf("account index %d is out of range; found %d account(s)", accountIndex, len(accounts))
	}
	data, err := session.TDesktopSession(accounts[accountIndex])
	if err != nil {
		return TDesktopImportResult{}, fmt.Errorf("convert Telegram Desktop session: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		return TDesktopImportResult{}, fmt.Errorf("prepare session dir: %w", err)
	}
	storage := &session.FileStorage{Path: sessionPath}
	loader := session.Loader{Storage: storage}
	if err := loader.Save(ctx, data); err != nil {
		return TDesktopImportResult{}, fmt.Errorf("save gotd session: %w", err)
	}
	return TDesktopImportResult{
		AccountCount: len(accounts),
		AccountIndex: accountIndex,
		SessionPath:  sessionPath,
		DC:           data.DC,
		Addr:         data.Addr,
	}, nil
}
