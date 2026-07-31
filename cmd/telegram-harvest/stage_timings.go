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

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-harvest/internal/stages"
)

var dailyTimingRunSequence atomic.Uint64

type dailyStageSeconds struct {
	TelegramScan   float64 `json:"telegram_scan"`
	Download       float64 `json:"download"`
	FFmpeg         float64 `json:"ffmpeg"`
	ModelColdStart float64 `json:"model_cold_start"`
	ASR            float64 `json:"asr"`
	Render         float64 `json:"render"`
}

type dailyStageTimingReport struct {
	RunID             string                        `json:"run_id"`
	Command           string                        `json:"command"`
	StartDate         string                        `json:"start_date,omitempty"`
	EndDate           string                        `json:"end_date,omitempty"`
	StartedAt         time.Time                     `json:"started_at"`
	CompletedAt       time.Time                     `json:"completed_at"`
	Success           bool                          `json:"success"`
	Error             string                        `json:"error,omitempty"`
	Stages            dailyStageSeconds             `json:"stages_seconds"`
	AudioSeconds      float64                       `json:"audio_seconds"`
	ASRSpeedX         float64                       `json:"asr_speed_x"`
	PipelineSpeedX    float64                       `json:"pipeline_speed_x"`
	MediaPipeline     *stages.MediaPipelineMetrics  `json:"media_pipeline,omitempty"`
	DialogCheckpoint  dailyDialogCheckpointMetrics  `json:"dialog_checkpoint"`
	HistoryPagination dailyHistoryPaginationMetrics `json:"history_pagination"`
	TelegramRPC       harvest.RPCPacingStats        `json:"telegram_rpc"`
	StageWorkSeconds  float64                       `json:"stage_work_seconds"`
	TotalSeconds      float64                       `json:"total_seconds"`
}

type dailyDialogCheckpointMetrics struct {
	Evaluated      bool   `json:"evaluated"`
	Enabled        bool   `json:"enabled"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	DialogsTotal   int    `json:"dialogs_total"`
	HistoryRPC     int    `json:"history_rpc"`
	Unchanged      int    `json:"unchanged"`
	Changed        int    `json:"changed"`
	New            int    `json:"new"`
}

type dailyHistoryPaginationMetrics struct {
	DataPages            int            `json:"data_pages"`
	EmptyProofPages      int            `json:"empty_proof_pages"`
	SparseContinuations  int            `json:"sparse_continuations"`
	ProofCandidates      int            `json:"checkpoint_proof_candidates"`
	ProofStops           int            `json:"checkpoint_proof_stops"`
	ProofShadowConfirmed int            `json:"checkpoint_proof_shadow_confirmed"`
	ProofShadowRejected  int            `json:"checkpoint_proof_shadow_rejected"`
	ProofRejections      map[string]int `json:"checkpoint_proof_rejections,omitempty"`
}

type dailyStageTimingCollector struct {
	mu                sync.Mutex
	runID             string
	command           string
	startDate         string
	endDate           string
	startedAt         time.Time
	durations         map[stages.Name]time.Duration
	audioSeconds      float64
	mediaPipeline     *stages.MediaPipelineMetrics
	dialogCheckpoint  dailyDialogCheckpointMetrics
	historyPagination dailyHistoryPaginationMetrics
	telegramRPC       harvest.RPCPacingStats
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

func (c *dailyStageTimingCollector) ObserveMediaPipeline(metrics stages.MediaPipelineMetrics) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := metrics
	c.mediaPipeline = &copy
}

func (c *dailyStageTimingCollector) ObserveDialogCheckpoint(decision harvest.DailyDialogCheckpointDecision, stats harvest.OutgoingStats) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dialogCheckpoint = dailyDialogCheckpointMetrics{
		Evaluated:      true,
		Enabled:        decision.Enabled,
		FallbackReason: decision.FallbackReason,
		DialogsTotal:   stats.DialogsScanned,
		HistoryRPC:     stats.DialogsHistoryRPC,
		Unchanged:      stats.DialogsUnchanged,
		Changed:        stats.DialogsChanged,
		New:            stats.DialogsNew,
	}
}

func (c *dailyStageTimingCollector) ObserveOutgoingStats(stats harvest.OutgoingStats) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.historyPagination = dailyHistoryPaginationMetrics{
		DataPages:            stats.HistoryDataPages,
		EmptyProofPages:      stats.HistoryEmptyProofPages,
		SparseContinuations:  stats.HistorySparseContinuations,
		ProofCandidates:      stats.CheckpointProofCandidates,
		ProofStops:           stats.CheckpointProofStops,
		ProofShadowConfirmed: stats.CheckpointProofShadowConfirmed,
		ProofShadowRejected:  stats.CheckpointProofShadowRejected,
		ProofRejections:      cloneStringIntMap(stats.CheckpointProofRejections),
	}
	c.telegramRPC = stats.RPCPacing
}

func cloneStringIntMap(source map[string]int) map[string]int {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
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
		ASR:            c.durations[stages.ASR].Seconds(),
		Render:         c.durations[stages.Render].Seconds(),
	}
	stageWork := stageSeconds.TelegramScan + stageSeconds.Download + stageSeconds.FFmpeg + stageSeconds.ModelColdStart + stageSeconds.ASR + stageSeconds.Render
	total := completedAt.Sub(c.startedAt).Seconds()
	asrSpeedX := speedRatio(c.audioSeconds, stageSeconds.ASR)
	pipelineSpeedX := speedRatio(c.audioSeconds, stageSeconds.ModelColdStart+stageSeconds.FFmpeg+stageSeconds.ASR)
	if c.mediaPipeline != nil {
		asrSpeedX = c.mediaPipeline.WorkerWorkSpeedX
		pipelineSpeedX = c.mediaPipeline.PoolSpeedX
	}
	report := dailyStageTimingReport{
		RunID:             c.runID,
		Command:           c.command,
		StartDate:         c.startDate,
		EndDate:           c.endDate,
		StartedAt:         c.startedAt,
		CompletedAt:       completedAt,
		Success:           runErr == nil,
		Stages:            stageSeconds,
		AudioSeconds:      c.audioSeconds,
		ASRSpeedX:         asrSpeedX,
		PipelineSpeedX:    pipelineSpeedX,
		MediaPipeline:     c.mediaPipeline,
		DialogCheckpoint:  c.dialogCheckpoint,
		HistoryPagination: c.historyPagination,
		TelegramRPC:       c.telegramRPC,
		StageWorkSeconds:  stageWork,
		TotalSeconds:      total,
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
		pipelineMode := ""
		pipelineSpan := 0.0
		pipelineOverlap := 0.0
		pipelineWorkers := 0
		pipelineQueuePeak := 0
		if report.MediaPipeline != nil {
			pipelineMode = report.MediaPipeline.Mode
			pipelineSpan = report.MediaPipeline.SpanSeconds
			pipelineOverlap = report.MediaPipeline.OverlapSeconds
			pipelineWorkers = report.MediaPipeline.WorkersPeak
			pipelineQueuePeak = report.MediaPipeline.QueuePeak
		}
		fmt.Fprintf(out,
			"timings telegram_scan=%.3fs download=%.3fs ffmpeg=%.3fs model_cold_start=%.3fs asr=%.3fs render=%.3fs stage_work=%.3fs audio=%.3fs asr_speed=%.2fx pipeline_speed=%.2fx pipeline_mode=%s pipeline_span=%.3fs pipeline_overlap=%.3fs pipeline_workers=%d pipeline_queue_peak=%d rpc_spacing_ms=%d rpc_calls=%d rpc_wait=%.3fs transport_floods=%d history_data_pages=%d history_empty_proof_pages=%d history_sparse_continuations=%d checkpoint_proof_candidates=%d checkpoint_proof_stops=%d checkpoint_proof_shadow_confirmed=%d checkpoint_proof_shadow_rejected=%d checkpoint_enabled=%t checkpoint_history_dialogs=%d checkpoint_unchanged=%d checkpoint_changed=%d checkpoint_new=%d checkpoint_fallback=%s total=%.3fs report=%s\n",
			report.Stages.TelegramScan,
			report.Stages.Download,
			report.Stages.FFmpeg,
			report.Stages.ModelColdStart,
			report.Stages.ASR,
			report.Stages.Render,
			report.StageWorkSeconds,
			report.AudioSeconds,
			report.ASRSpeedX,
			report.PipelineSpeedX,
			pipelineMode,
			pipelineSpan,
			pipelineOverlap,
			pipelineWorkers,
			pipelineQueuePeak,
			report.TelegramRPC.SpacingMillis,
			report.TelegramRPC.Calls,
			report.TelegramRPC.ScheduledWaitSeconds,
			report.TelegramRPC.TransportFloods,
			report.HistoryPagination.DataPages,
			report.HistoryPagination.EmptyProofPages,
			report.HistoryPagination.SparseContinuations,
			report.HistoryPagination.ProofCandidates,
			report.HistoryPagination.ProofStops,
			report.HistoryPagination.ProofShadowConfirmed,
			report.HistoryPagination.ProofShadowRejected,
			report.DialogCheckpoint.Enabled,
			report.DialogCheckpoint.HistoryRPC,
			report.DialogCheckpoint.Unchanged,
			report.DialogCheckpoint.Changed,
			report.DialogCheckpoint.New,
			report.DialogCheckpoint.FallbackReason,
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
