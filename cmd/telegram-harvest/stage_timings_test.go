package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/stages"
)

func TestDailyStageTimingReportPersistsAllStagesWithoutOverwrite(t *testing.T) {
	stateDir := t.TempDir()
	first := newDailyStageTimingCollector("daily-catchup", "2026-07-22", "2026-07-29")
	first.Observe(stages.TelegramScan, 3*time.Second)
	first.Observe(stages.Download, 2*time.Second)
	first.Observe(stages.FFmpeg, time.Second)
	first.Observe(stages.Vosk, 4*time.Second)
	first.Observe(stages.Render, 500*time.Millisecond)
	first.startedAt = time.Now().UTC().Add(-11 * time.Second)

	firstReport := first.Report(nil)
	firstPath, err := writeDailyStageTimingReport(stateDir, firstReport)
	if err != nil {
		t.Fatal(err)
	}
	second := newDailyStageTimingCollector("daily-catchup", "2026-07-22", "2026-07-29")
	second.startedAt = time.Now().UTC().Add(-time.Second)
	secondReport := second.Report(errors.New("telegram unavailable"))
	secondPath, err := writeDailyStageTimingReport(stateDir, secondReport)
	if err != nil {
		t.Fatal(err)
	}
	if firstPath == secondPath {
		t.Fatalf("timing reports reused path %s", firstPath)
	}
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("first report was lost: %v", err)
	}

	content, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded dailyStageTimingReport
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Stages.TelegramScan != 3 || decoded.Stages.Download != 2 || decoded.Stages.FFmpeg != 1 || decoded.Stages.Vosk != 4 || decoded.Stages.Render != 0.5 {
		t.Fatalf("stages = %+v", decoded.Stages)
	}
	if decoded.UnaccountedSeconds <= 0 {
		t.Fatalf("unaccounted = %f, want positive remainder", decoded.UnaccountedSeconds)
	}
	if decoded.Command != "daily-catchup" || decoded.StartDate != "2026-07-22" || decoded.EndDate != "2026-07-29" || !decoded.Success {
		t.Fatalf("report metadata = %+v", decoded)
	}
	if secondReport.Success || !strings.Contains(secondReport.Error, "telegram unavailable") {
		t.Fatalf("failed report = %+v", secondReport)
	}
	if filepath.Dir(firstPath) != filepath.Join(stateDir, "timings") {
		t.Fatalf("report dir = %s", filepath.Dir(firstPath))
	}
}

func TestFinishDailyStageTimingsPrintsFiveStagesAndReportPath(t *testing.T) {
	stateDir := t.TempDir()
	collector := newDailyStageTimingCollector("daily", "2026-07-29", "2026-07-29")
	var output strings.Builder
	if err := finishDailyStageTimings(stateDir, collector, nil, &output); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"telegram_scan=", "download=", "ffmpeg=", "vosk=", "render=", "unaccounted=", "total=", "report="} {
		if !strings.Contains(output.String(), field) {
			t.Fatalf("missing %q in %s", field, output.String())
		}
	}
	entries, err := os.ReadDir(filepath.Join(stateDir, "timings"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("timing report count = %d", len(entries))
	}
}
