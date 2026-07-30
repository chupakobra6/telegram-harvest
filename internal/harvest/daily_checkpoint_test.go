package harvest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEvaluateDailyDialogCheckpointRequiresSafeContinuation(t *testing.T) {
	scope := DailyDialogScopeFingerprint(DailyDialogCheckpointScope{
		Version:     DailyDialogCheckpointVersion,
		DialogLimit: 500,
		AdditionalSenders: []DailyDialogScopeSenderRef{
			{ChatID: 20, SenderID: 2},
			{ChatID: 10, SenderID: 1},
		},
	})
	checkpoint := NewDailyDialogCheckpoint(42, scope, "2026-07-28", []DailyDialogHead{
		{ChatID: 100, ChatType: "user", TopMessageID: 7, VerifiedMessageID: 7, HeadFullyVerified: true},
		{ChatID: 100, ChatType: "supergroup", TopMessageID: 9, VerifiedMessageID: 9, HeadFullyVerified: true},
	}, time.Now())

	decision := EvaluateDailyDialogCheckpoint(checkpoint, nil, 42, scope, "2026-07-29")
	if !decision.Enabled || decision.FallbackReason != "" || len(decision.Dialogs) != 2 {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.Dialogs[DailyDialogHeadKey("user", 100)].TopMessageID != 7 {
		t.Fatalf("user checkpoint missing: %+v", decision.Dialogs)
	}
	if decision.Dialogs[DailyDialogHeadKey("supergroup", 100)].TopMessageID != 9 {
		t.Fatalf("supergroup checkpoint missing: %+v", decision.Dialogs)
	}

	tests := []struct {
		name    string
		state   DailyDialogCheckpoint
		loadErr error
		account int64
		scope   string
		start   string
		reason  string
	}{
		{name: "missing", account: 42, scope: scope, start: "2026-07-29", reason: "state_missing"},
		{name: "invalid file", state: checkpoint, loadErr: errors.New("bad json"), account: 42, scope: scope, start: "2026-07-29", reason: "state_invalid"},
		{name: "version mismatch", state: withCheckpointVersion(checkpoint, DailyDialogCheckpointVersion-1), account: 42, scope: scope, start: "2026-07-29", reason: "version_mismatch"},
		{name: "incomplete", state: withCheckpointComplete(checkpoint, false), account: 42, scope: scope, start: "2026-07-29", reason: "previous_incomplete"},
		{name: "account mismatch", state: checkpoint, account: 43, scope: scope, start: "2026-07-29", reason: "account_mismatch"},
		{name: "scope mismatch", state: checkpoint, account: 42, scope: "other", start: "2026-07-29", reason: "scope_mismatch"},
		{name: "historical", state: checkpoint, account: 42, scope: scope, start: "2026-07-28", reason: "range_not_contiguous"},
		{name: "gap", state: checkpoint, account: 42, scope: scope, start: "2026-07-30", reason: "range_not_contiguous"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateDailyDialogCheckpoint(tc.state, tc.loadErr, tc.account, tc.scope, tc.start)
			if got.Enabled || got.FallbackReason != tc.reason || len(got.Dialogs) != 0 {
				t.Fatalf("decision = %+v, want fallback %q", got, tc.reason)
			}
		})
	}
}

func TestDailyDialogScopeFingerprintIsOrderIndependentAndScopeSensitive(t *testing.T) {
	left := DailyDialogCheckpointScope{
		Version:     1,
		DialogLimit: 500,
		AdditionalSenders: []DailyDialogScopeSenderRef{
			{ChatID: 2, SenderID: 20},
			{ChatID: 1, SenderID: 10},
		},
	}
	right := left
	right.AdditionalSenders = []DailyDialogScopeSenderRef{
		{ChatID: 1, SenderID: 10},
		{ChatID: 2, SenderID: 20},
	}
	if DailyDialogScopeFingerprint(left) != DailyDialogScopeFingerprint(right) {
		t.Fatal("sender order changed the scope fingerprint")
	}
	right.IncludeService = true
	if DailyDialogScopeFingerprint(left) == DailyDialogScopeFingerprint(right) {
		t.Fatal("scope change did not change fingerprint")
	}
}

func TestSaveDailyDialogCheckpointIsAtomicAndRejectsIncompleteState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", DailyDialogCheckpointFilename)
	old := NewDailyDialogCheckpoint(42, "scope", "2026-07-28", []DailyDialogHead{
		{ChatID: 1, ChatType: "user", TopMessageID: 2, VerifiedMessageID: 2, HeadFullyVerified: true},
	}, time.Unix(1, 0))
	if err := SaveDailyDialogCheckpoint(path, old); err != nil {
		t.Fatal(err)
	}
	incomplete := old
	incomplete.Complete = false
	if err := SaveDailyDialogCheckpoint(path, incomplete); err == nil {
		t.Fatal("incomplete checkpoint was saved")
	}
	got, err := LoadDailyDialogCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Complete || got.VerifiedThrough != old.VerifiedThrough || got.Dialogs[0].TopMessageID != 2 {
		t.Fatalf("checkpoint changed after rejected save: %+v", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != DailyDialogCheckpointFilename {
		t.Fatalf("unexpected checkpoint files: %+v", entries)
	}
}

func withCheckpointComplete(checkpoint DailyDialogCheckpoint, complete bool) DailyDialogCheckpoint {
	checkpoint.Complete = complete
	return checkpoint
}

func withCheckpointVersion(checkpoint DailyDialogCheckpoint, version int) DailyDialogCheckpoint {
	checkpoint.Version = version
	return checkpoint
}
