package harvest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const DailyDialogCheckpointVersion = 2
const DailyDialogCheckpointFilename = "dialog-checkpoint.json"

type DailyDialogCheckpoint struct {
	Version          int               `json:"version"`
	AccountID        int64             `json:"account_id"`
	ScopeFingerprint string            `json:"scope_fingerprint"`
	VerifiedThrough  string            `json:"verified_through"`
	Complete         bool              `json:"complete"`
	UpdatedAt        time.Time         `json:"updated_at"`
	Dialogs          []DailyDialogHead `json:"dialogs"`
}

type DailyDialogHead struct {
	ChatID            int64  `json:"chat_id"`
	ChatType          string `json:"chat_type"`
	TopMessageID      int    `json:"top_message_id"`
	VerifiedMessageID int    `json:"verified_message_id"`
	HeadFullyVerified bool   `json:"head_fully_verified"`
	VerifiedThrough   string `json:"verified_through"`
}

type DailyDialogCheckpointScope struct {
	Version           int                         `json:"version"`
	DialogLimit       int                         `json:"dialog_limit"`
	IncludeService    bool                        `json:"include_service"`
	AdditionalSenders []DailyDialogScopeSenderRef `json:"additional_senders,omitempty"`
}

type DailyDialogScopeSenderRef struct {
	ChatID   int64 `json:"chat_id"`
	SenderID int64 `json:"sender_id"`
}

type DailyDialogCheckpointDecision struct {
	Enabled        bool
	FallbackReason string
	Dialogs        map[string]DailyDialogHead
}

func DailyDialogCheckpointPath(stateDir string) string {
	return filepath.Join(stateDir, DailyDialogCheckpointFilename)
}

func LoadDailyDialogCheckpoint(path string) (DailyDialogCheckpoint, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DailyDialogCheckpoint{}, nil
		}
		return DailyDialogCheckpoint{}, fmt.Errorf("read daily dialog checkpoint: %w", err)
	}
	var checkpoint DailyDialogCheckpoint
	if err := json.Unmarshal(content, &checkpoint); err != nil {
		return DailyDialogCheckpoint{}, fmt.Errorf("parse daily dialog checkpoint: %w", err)
	}
	return checkpoint, nil
}

func SaveDailyDialogCheckpoint(path string, checkpoint DailyDialogCheckpoint) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("daily dialog checkpoint path is required")
	}
	if checkpoint.Version != DailyDialogCheckpointVersion {
		return fmt.Errorf("daily dialog checkpoint version = %d, want %d", checkpoint.Version, DailyDialogCheckpointVersion)
	}
	if checkpoint.AccountID == 0 {
		return fmt.Errorf("daily dialog checkpoint account id is required")
	}
	if strings.TrimSpace(checkpoint.ScopeFingerprint) == "" {
		return fmt.Errorf("daily dialog checkpoint scope fingerprint is required")
	}
	if _, err := parseDailyCheckpointDate(checkpoint.VerifiedThrough); err != nil {
		return err
	}
	if !checkpoint.Complete {
		return fmt.Errorf("refusing to save incomplete daily dialog checkpoint")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("prepare daily dialog checkpoint dir: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create daily dialog checkpoint temp file: %w", err)
	}
	tempPath := temp.Name()
	published := false
	defer func() {
		_ = temp.Close()
		if !published {
			_ = os.Remove(tempPath)
		}
	}()
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(checkpoint); err != nil {
		return fmt.Errorf("encode daily dialog checkpoint: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync daily dialog checkpoint: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close daily dialog checkpoint: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish daily dialog checkpoint: %w", err)
	}
	published = true
	return nil
}

func DailyDialogScopeFingerprint(scope DailyDialogCheckpointScope) string {
	scope.AdditionalSenders = append([]DailyDialogScopeSenderRef(nil), scope.AdditionalSenders...)
	sort.Slice(scope.AdditionalSenders, func(i, j int) bool {
		if scope.AdditionalSenders[i].ChatID != scope.AdditionalSenders[j].ChatID {
			return scope.AdditionalSenders[i].ChatID < scope.AdditionalSenders[j].ChatID
		}
		return scope.AdditionalSenders[i].SenderID < scope.AdditionalSenders[j].SenderID
	})
	content, _ := json.Marshal(scope)
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func EvaluateDailyDialogCheckpoint(
	checkpoint DailyDialogCheckpoint,
	loadErr error,
	accountID int64,
	scopeFingerprint string,
	rangeStart string,
) DailyDialogCheckpointDecision {
	fallback := func(reason string) DailyDialogCheckpointDecision {
		return DailyDialogCheckpointDecision{FallbackReason: reason}
	}
	if loadErr != nil {
		return fallback("state_invalid")
	}
	if checkpoint.Version == 0 {
		return fallback("state_missing")
	}
	if checkpoint.Version != DailyDialogCheckpointVersion {
		return fallback("version_mismatch")
	}
	if !checkpoint.Complete {
		return fallback("previous_incomplete")
	}
	if checkpoint.AccountID == 0 || accountID == 0 || checkpoint.AccountID != accountID {
		return fallback("account_mismatch")
	}
	if strings.TrimSpace(checkpoint.ScopeFingerprint) == "" || checkpoint.ScopeFingerprint != scopeFingerprint {
		return fallback("scope_mismatch")
	}
	verifiedThrough, err := parseDailyCheckpointDate(checkpoint.VerifiedThrough)
	if err != nil {
		return fallback("verified_through_invalid")
	}
	start, err := parseDailyCheckpointDate(rangeStart)
	if err != nil {
		return fallback("range_start_invalid")
	}
	if !verifiedThrough.AddDate(0, 0, 1).Equal(start) {
		return fallback("range_not_contiguous")
	}
	dialogs := make(map[string]DailyDialogHead, len(checkpoint.Dialogs))
	for _, dialog := range checkpoint.Dialogs {
		if dialog.ChatID == 0 ||
			strings.TrimSpace(dialog.ChatType) == "" ||
			dialog.TopMessageID < 0 ||
			dialog.VerifiedMessageID < 0 ||
			dialog.VerifiedMessageID > dialog.TopMessageID ||
			(dialog.HeadFullyVerified && dialog.VerifiedMessageID != dialog.TopMessageID) ||
			dialog.VerifiedThrough != checkpoint.VerifiedThrough {
			return fallback("dialog_state_invalid")
		}
		key := DailyDialogHeadKey(dialog.ChatType, dialog.ChatID)
		if _, exists := dialogs[key]; exists {
			return fallback("dialog_state_invalid")
		}
		dialogs[key] = dialog
	}
	return DailyDialogCheckpointDecision{Enabled: true, Dialogs: dialogs}
}

func NewDailyDialogCheckpoint(
	accountID int64,
	scopeFingerprint string,
	verifiedThrough string,
	heads []DailyDialogHead,
	now time.Time,
) DailyDialogCheckpoint {
	dialogs := make([]DailyDialogHead, 0, len(heads))
	for _, head := range heads {
		head.VerifiedThrough = verifiedThrough
		dialogs = append(dialogs, head)
	}
	sort.Slice(dialogs, func(i, j int) bool {
		if dialogs[i].ChatType != dialogs[j].ChatType {
			return dialogs[i].ChatType < dialogs[j].ChatType
		}
		return dialogs[i].ChatID < dialogs[j].ChatID
	})
	return DailyDialogCheckpoint{
		Version:          DailyDialogCheckpointVersion,
		AccountID:        accountID,
		ScopeFingerprint: scopeFingerprint,
		VerifiedThrough:  verifiedThrough,
		Complete:         true,
		UpdatedAt:        now.UTC(),
		Dialogs:          dialogs,
	}
}

func DailyDialogHeadKey(chatType string, chatID int64) string {
	return strings.TrimSpace(chatType) + ":" + fmt.Sprintf("%d", chatID)
}

func parseDailyCheckpointDate(value string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid daily dialog checkpoint date %q: %w", value, err)
	}
	return parsed, nil
}
