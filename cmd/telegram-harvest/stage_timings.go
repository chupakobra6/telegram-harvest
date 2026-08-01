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
	RunID             string                          `json:"run_id"`
	Command           string                          `json:"command"`
	StartDate         string                          `json:"start_date,omitempty"`
	EndDate           string                          `json:"end_date,omitempty"`
	StartedAt         time.Time                       `json:"started_at"`
	CompletedAt       time.Time                       `json:"completed_at"`
	Success           bool                            `json:"success"`
	Error             string                          `json:"error,omitempty"`
	Stages            dailyStageSeconds               `json:"stages_seconds"`
	AudioSeconds      float64                         `json:"audio_seconds"`
	ASRSpeedX         float64                         `json:"asr_speed_x"`
	PipelineSpeedX    float64                         `json:"pipeline_speed_x"`
	MediaPipeline     *stages.MediaPipelineMetrics    `json:"media_pipeline,omitempty"`
	DownloadTransport stages.DownloadTransportMetrics `json:"download_transport"`
	DialogCheckpoint  dailyDialogCheckpointMetrics    `json:"dialog_checkpoint"`
	HistoryPagination dailyHistoryPaginationMetrics   `json:"history_pagination"`
	TelegramRPC       harvest.RPCPacingStats          `json:"telegram_rpc"`
	TelegramBreakdown dailyTelegramBreakdown          `json:"telegram_breakdown"`
	StageWorkSeconds  float64                         `json:"stage_work_seconds"`
	TotalSeconds      float64                         `json:"total_seconds"`
}

type dailyTelegramBreakdown struct {
	GetDialogsCalls          int     `json:"get_dialogs_calls"`
	GetDialogsWallSeconds    float64 `json:"get_dialogs_wall_seconds"`
	GetDialogsWaitSeconds    float64 `json:"get_dialogs_wait_seconds"`
	GetHistoryCalls          int     `json:"get_history_calls"`
	GetHistoryWallSeconds    float64 `json:"get_history_wall_seconds"`
	GetHistoryWaitSeconds    float64 `json:"get_history_wait_seconds"`
	RPCServiceSeconds        float64 `json:"rpc_service_seconds"`
	DownloadQueueWaitSeconds float64 `json:"download_queue_wait_seconds"`
	DownloadTransferSeconds  float64 `json:"download_transfer_seconds"`
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
	downloadTransport stages.DownloadTransportMetrics
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
	copy.Workers = append([]stages.MediaWorkerMetrics(nil), metrics.Workers...)
	c.mediaPipeline = &copy
}

func (c *dailyStageTimingCollector) ObserveDownloadTransfer(metrics stages.DownloadTransferMetrics) {
	if c == nil || metrics.Threads < 1 || metrics.Seconds < 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.downloadTransport.ThreadDecisions == nil {
		c.downloadTransport.ThreadDecisions = make(map[string]int, 3)
	}
	c.downloadTransport.Policy = metrics.Policy
	c.downloadTransport.Files++
	if metrics.Failed {
		c.downloadTransport.Failed++
	}
	c.downloadTransport.ExpectedBytes += metrics.ExpectedBytes
	c.downloadTransport.TransferredBytes += metrics.TransferredBytes
	c.downloadTransport.Seconds += metrics.Seconds
	if metrics.Threads > c.downloadTransport.PeakThreads {
		c.downloadTransport.PeakThreads = metrics.Threads
	}
	c.downloadTransport.ThreadDecisions[fmt.Sprintf("%d", metrics.Threads)]++
	c.downloadTransport.Retries += metrics.Retries
	c.downloadTransport.DownloaderFloods += metrics.FloodWaits
	c.downloadTransport.DownloaderTransportFloods += metrics.TransportFloods
}

func (c *dailyStageTimingCollector) ObserveDownloadQueueWait(duration time.Duration) {
	if c == nil || duration < 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.downloadTransport.QueueWaitSeconds += duration.Seconds()
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
	c.telegramRPC = cloneRPCPacingStats(stats.RPCPacing)
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

func cloneRPCPacingStats(source harvest.RPCPacingStats) harvest.RPCPacingStats {
	result := source
	result.Operations = cloneStringIntMap(source.Operations)
	if len(source.OperationTimings) == 0 {
		result.OperationTimings = nil
		return result
	}
	result.OperationTimings = make(map[string]harvest.RPCOperationTiming, len(source.OperationTimings))
	for operation, timing := range source.OperationTimings {
		result.OperationTimings[operation] = timing
	}
	return result
}

func cloneDownloadTransportMetrics(source stages.DownloadTransportMetrics) stages.DownloadTransportMetrics {
	result := source
	result.ThreadDecisions = cloneStringIntMap(source.ThreadDecisions)
	return result
}

func cloneMediaPipelineMetrics(source *stages.MediaPipelineMetrics) *stages.MediaPipelineMetrics {
	if source == nil {
		return nil
	}
	result := *source
	result.Workers = append([]stages.MediaWorkerMetrics(nil), source.Workers...)
	return &result
}

func buildDailyTelegramBreakdown(rpc harvest.RPCPacingStats, download stages.DownloadTransportMetrics) dailyTelegramBreakdown {
	dialogs := rpc.OperationTimings["get_dialogs"]
	history := rpc.OperationTimings["get_history"]
	return dailyTelegramBreakdown{
		GetDialogsCalls:          dialogs.Calls,
		GetDialogsWallSeconds:    dialogs.WallSeconds,
		GetDialogsWaitSeconds:    dialogs.WaitSeconds,
		GetHistoryCalls:          history.Calls,
		GetHistoryWallSeconds:    history.WallSeconds,
		GetHistoryWaitSeconds:    history.WaitSeconds,
		RPCServiceSeconds:        rpc.ServiceSeconds,
		DownloadQueueWaitSeconds: download.QueueWaitSeconds,
		DownloadTransferSeconds:  download.Seconds,
	}
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
	downloadTransport := cloneDownloadTransportMetrics(c.downloadTransport)
	if downloadTransport.Seconds > 0 && downloadTransport.TransferredBytes > 0 {
		downloadTransport.ThroughputMiBPerS = float64(downloadTransport.TransferredBytes) / (1024 * 1024) / downloadTransport.Seconds
	}
	mediaPipeline := cloneMediaPipelineMetrics(c.mediaPipeline)
	if mediaPipeline != nil {
		asrSpeedX = mediaPipeline.WorkerWorkSpeedX
		pipelineSpeedX = mediaPipeline.PoolSpeedX
	}
	telegramRPC := cloneRPCPacingStats(c.telegramRPC)
	historyPagination := c.historyPagination
	historyPagination.ProofRejections = cloneStringIntMap(c.historyPagination.ProofRejections)
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
		MediaPipeline:     mediaPipeline,
		DownloadTransport: downloadTransport,
		DialogCheckpoint:  c.dialogCheckpoint,
		HistoryPagination: historyPagination,
		TelegramRPC:       telegramRPC,
		TelegramBreakdown: buildDailyTelegramBreakdown(telegramRPC, downloadTransport),
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
			"timings telegram_scan=%.3fs download=%.3fs download_files=%d download_bytes=%d download_mib_s=%.2f download_peak_threads=%d download_retries=%d download_floods=%d download_transport_floods=%d download_queue_wait=%.3fs download_transfer=%.3fs ffmpeg=%.3fs model_cold_start=%.3fs asr=%.3fs render=%.3fs stage_work=%.3fs audio=%.3fs asr_speed=%.2fx pipeline_speed=%.2fx pipeline_mode=%s pipeline_span=%.3fs pipeline_overlap=%.3fs pipeline_workers=%d pipeline_queue_peak=%d rpc_spacing_ms=%d rpc_calls=%d rpc_wait=%.3fs rpc_service=%.3fs get_dialogs_calls=%d get_dialogs_wall=%.3fs get_dialogs_wait=%.3fs get_history_calls=%d get_history_wall=%.3fs get_history_wait=%.3fs transport_floods=%d history_data_pages=%d history_empty_proof_pages=%d history_sparse_continuations=%d checkpoint_proof_candidates=%d checkpoint_proof_stops=%d checkpoint_proof_shadow_confirmed=%d checkpoint_proof_shadow_rejected=%d checkpoint_enabled=%t checkpoint_history_dialogs=%d checkpoint_unchanged=%d checkpoint_changed=%d checkpoint_new=%d checkpoint_fallback=%s total=%.3fs report=%s\n",
			report.Stages.TelegramScan,
			report.Stages.Download,
			report.DownloadTransport.Files,
			report.DownloadTransport.TransferredBytes,
			report.DownloadTransport.ThroughputMiBPerS,
			report.DownloadTransport.PeakThreads,
			report.DownloadTransport.Retries,
			report.DownloadTransport.DownloaderFloods,
			report.DownloadTransport.DownloaderTransportFloods,
			report.TelegramBreakdown.DownloadQueueWaitSeconds,
			report.TelegramBreakdown.DownloadTransferSeconds,
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
			report.TelegramBreakdown.RPCServiceSeconds,
			report.TelegramBreakdown.GetDialogsCalls,
			report.TelegramBreakdown.GetDialogsWallSeconds,
			report.TelegramBreakdown.GetDialogsWaitSeconds,
			report.TelegramBreakdown.GetHistoryCalls,
			report.TelegramBreakdown.GetHistoryWallSeconds,
			report.TelegramBreakdown.GetHistoryWaitSeconds,
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
