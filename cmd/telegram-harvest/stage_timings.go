package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/stages"
)

var dailyTimingRunSequence atomic.Uint64

type dailyStageSeconds struct {
	TelegramScan   float64 `json:"telegram_scan"`
	Download       float64 `json:"download"`
	FFmpeg         float64 `json:"ffmpeg"`
	ModelColdStart float64 `json:"model_cold_start"`
	Vosk           float64 `json:"vosk"`
	Render         float64 `json:"render"`
}

type dailyStageTimingReport struct {
	RunID              string            `json:"run_id"`
	Command            string            `json:"command"`
	StartDate          string            `json:"start_date,omitempty"`
	EndDate            string            `json:"end_date,omitempty"`
	StartedAt          time.Time         `json:"started_at"`
	CompletedAt        time.Time         `json:"completed_at"`
	Success            bool              `json:"success"`
	Error              string            `json:"error,omitempty"`
	Stages             dailyStageSeconds `json:"stages_seconds"`
	AudioSeconds       float64           `json:"audio_seconds"`
	ASRSpeedX          float64           `json:"asr_speed_x"`
	PipelineSpeedX     float64           `json:"pipeline_speed_x"`
	AccountedSeconds   float64           `json:"accounted_seconds"`
	UnaccountedSeconds float64           `json:"unaccounted_seconds"`
	TotalSeconds       float64           `json:"total_seconds"`
}

type dailyStageTimingCollector struct {
	mu           sync.Mutex
	runID        string
	command      string
	startDate    string
	endDate      string
	startedAt    time.Time
	durations    map[stages.Name]time.Duration
	audioSeconds float64
}

func newDailyStageTimingCollector(command string, startDate string, endDate string) *dailyStageTimingCollector {
	startedAt := time.Now().UTC()
	return &dailyStageTimingCollector{
		runID:     fmt.Sprintf("%s-%d-%d", startedAt.Format("20060102T150405.000000000Z"), os.Getpid(), dailyTimingRunSequence.Add(1)),
		command:   command,
		startDate: startDate,
		endDate:   endDate,
		startedAt: startedAt,
		durations: make(map[stages.Name]time.Duration, 6),
	}
}

func (c *dailyStageTimingCollector) Observe(stage stages.Name, duration time.Duration) {
	if c == nil || duration < 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.durations[stage] += duration
}

func (c *dailyStageTimingCollector) ObserveAudioDuration(seconds float64) {
	if c == nil || seconds <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.audioSeconds += seconds
}

func (c *dailyStageTimingCollector) Report(runErr error) dailyStageTimingReport {
	completedAt := time.Now().UTC()
	if c == nil {
		return dailyStageTimingReport{CompletedAt: completedAt, Success: runErr == nil}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	stageSeconds := dailyStageSeconds{
		TelegramScan:   c.durations[stages.TelegramScan].Seconds(),
		Download:       c.durations[stages.Download].Seconds(),
		FFmpeg:         c.durations[stages.FFmpeg].Seconds(),
		ModelColdStart: c.durations[stages.ModelColdStart].Seconds(),
		Vosk:           c.durations[stages.Vosk].Seconds(),
		Render:         c.durations[stages.Render].Seconds(),
	}
	accounted := stageSeconds.TelegramScan + stageSeconds.Download + stageSeconds.FFmpeg + stageSeconds.ModelColdStart + stageSeconds.Vosk + stageSeconds.Render
	total := completedAt.Sub(c.startedAt).Seconds()
	unaccounted := total - accounted
	asrSpeedX := speedRatio(c.audioSeconds, stageSeconds.Vosk)
	pipelineSpeedX := speedRatio(c.audioSeconds, stageSeconds.ModelColdStart+stageSeconds.FFmpeg+stageSeconds.Vosk)
	report := dailyStageTimingReport{
		RunID:              c.runID,
		Command:            c.command,
		StartDate:          c.startDate,
		EndDate:            c.endDate,
		StartedAt:          c.startedAt,
		CompletedAt:        completedAt,
		Success:            runErr == nil,
		Stages:             stageSeconds,
		AudioSeconds:       c.audioSeconds,
		ASRSpeedX:          asrSpeedX,
		PipelineSpeedX:     pipelineSpeedX,
		AccountedSeconds:   accounted,
		UnaccountedSeconds: unaccounted,
		TotalSeconds:       total,
	}
	if runErr != nil {
		report.Error = strings.TrimSpace(runErr.Error())
	}
	return report
}

func speedRatio(audioSeconds float64, workSeconds float64) float64 {
	if audioSeconds <= 0 || workSeconds <= 0 {
		return 0
	}
	return audioSeconds / workSeconds
}

func finishDailyStageTimings(stateDir string, collector *dailyStageTimingCollector, runErr error, out io.Writer) error {
	report := collector.Report(runErr)
	path, persistErr := writeDailyStageTimingReport(stateDir, report)
	if persistErr == nil {
		fmt.Fprintf(out,
			"timings telegram_scan=%.3fs download=%.3fs ffmpeg=%.3fs model_cold_start=%.3fs vosk=%.3fs render=%.3fs audio=%.3fs asr_speed=%.2fx pipeline_speed=%.2fx unaccounted=%.3fs total=%.3fs report=%s\n",
			report.Stages.TelegramScan,
			report.Stages.Download,
			report.Stages.FFmpeg,
			report.Stages.ModelColdStart,
			report.Stages.Vosk,
			report.Stages.Render,
			report.AudioSeconds,
			report.ASRSpeedX,
			report.PipelineSpeedX,
			report.UnaccountedSeconds,
			report.TotalSeconds,
			path,
		)
	}
	return errors.Join(runErr, persistErr)
}

func writeDailyStageTimingReport(stateDir string, report dailyStageTimingReport) (string, error) {
	if strings.TrimSpace(stateDir) == "" {
		return "", fmt.Errorf("state dir is required for stage timing report")
	}
	fileName := report.RunID + "-" + safeTimingName(report.Command) + ".json"
	path := filepath.Join(stateDir, "timings", fileName)
	tempPath, file, err := createAtomicOutput(path)
	if err != nil {
		return "", err
	}
	published := false
	defer func() {
		_ = file.Close()
		if !published {
			_ = os.Remove(tempPath)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := publishAtomicOutput(tempPath, path); err != nil {
		return "", err
	}
	published = true
	return path, nil
}

func safeTimingName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "daily"
	}
	return strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(value)
}
