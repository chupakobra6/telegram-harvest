package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-harvest/internal/stages"
)

func TestDailyStageTimingReportPersistsAllStagesWithoutOverwrite(t *testing.T) {
	stateDir := t.TempDir()
	first := newDailyStageTimingCollector("daily-catchup", "2026-07-22", "2026-07-29")
	first.Observe(stages.TelegramScan, 3*time.Second)
	first.Observe(stages.Download, 2*time.Second)
	first.Observe(stages.FFmpeg, time.Second)
	first.Observe(stages.ModelColdStart, 2*time.Second)
	first.Observe(stages.ASR, 4*time.Second)
	first.Observe(stages.Render, 500*time.Millisecond)
	first.ObserveAudioDuration(24)
	first.ObserveDownloadTransfer(stages.DownloadTransferMetrics{
		Policy:           "size-tiered",
		ExpectedBytes:    8 << 20,
		TransferredBytes: 8 << 20,
		Threads:          2,
		Seconds:          2,
	})
	first.ObserveDownloadQueueWait(750 * time.Millisecond)
	first.ObserveDialogCheckpoint(
		harvest.DailyDialogCheckpointDecision{Enabled: true},
		harvest.OutgoingStats{
			DialogsScanned:    10,
			DialogsHistoryRPC: 2,
			DialogsUnchanged:  8,
			DialogsChanged:    1,
			DialogsNew:        1,
		},
	)
	first.ObserveOutgoingStats(harvest.OutgoingStats{
		HistoryDataPages:               3,
		HistoryEmptyProofPages:         2,
		HistorySparseContinuations:     1,
		CheckpointProofCandidates:      2,
		CheckpointProofStops:           2,
		CheckpointProofShadowConfirmed: 4,
		CheckpointProofShadowRejected:  0,
		CheckpointProofRejections:      map[string]int{"head_mismatch": 1},
		RPCPacing: harvest.RPCPacingStats{
			SpacingMillis:        500,
			Calls:                12,
			ScheduledWaitSeconds: 5.5,
			ServiceSeconds:       1.75,
			Operations:           map[string]int{"get_history": 7, "get_dialogs": 5},
			OperationTimings: map[string]harvest.RPCOperationTiming{
				"get_dialogs": {Calls: 5, WallSeconds: 3, WaitSeconds: 2.5, ServiceSeconds: 0.5},
				"get_history": {Calls: 7, WallSeconds: 5, WaitSeconds: 3.75, ServiceSeconds: 1.25},
			},
		},
	})
	first.startedAt = time.Now().UTC().Add(-13 * time.Second)

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
	if decoded.Stages.TelegramScan != 3 || decoded.Stages.Download != 2 || decoded.Stages.FFmpeg != 1 || decoded.Stages.ModelColdStart != 2 || decoded.Stages.ASR != 4 || decoded.Stages.Render != 0.5 {
		t.Fatalf("stages = %+v", decoded.Stages)
	}
	if decoded.AudioSeconds != 24 {
		t.Fatalf("audio_seconds = %f, want 24", decoded.AudioSeconds)
	}
	if decoded.ASRSpeedX != 6 {
		t.Fatalf("asr_speed_x = %f, want 6", decoded.ASRSpeedX)
	}
	if decoded.PipelineSpeedX != 24.0/7.0 {
		t.Fatalf("pipeline_speed_x = %f, want %f", decoded.PipelineSpeedX, 24.0/7.0)
	}
	if !decoded.DialogCheckpoint.Evaluated || !decoded.DialogCheckpoint.Enabled ||
		decoded.DialogCheckpoint.DialogsTotal != 10 || decoded.DialogCheckpoint.HistoryRPC != 2 ||
		decoded.DialogCheckpoint.Unchanged != 8 || decoded.DialogCheckpoint.Changed != 1 || decoded.DialogCheckpoint.New != 1 {
		t.Fatalf("dialog checkpoint metrics = %+v", decoded.DialogCheckpoint)
	}
	if decoded.HistoryPagination.DataPages != 3 || decoded.HistoryPagination.EmptyProofPages != 2 ||
		decoded.HistoryPagination.SparseContinuations != 1 || decoded.HistoryPagination.ProofCandidates != 2 ||
		decoded.HistoryPagination.ProofStops != 2 || decoded.HistoryPagination.ProofShadowConfirmed != 4 ||
		decoded.HistoryPagination.ProofShadowRejected != 0 || decoded.HistoryPagination.ProofRejections["head_mismatch"] != 1 {
		t.Fatalf("history pagination metrics = %+v", decoded.HistoryPagination)
	}
	if decoded.TelegramRPC.SpacingMillis != 500 || decoded.TelegramRPC.Calls != 12 ||
		decoded.TelegramRPC.ScheduledWaitSeconds != 5.5 || decoded.TelegramRPC.ServiceSeconds != 1.75 ||
		decoded.TelegramRPC.Operations["get_history"] != 7 || decoded.TelegramRPC.OperationTimings["get_history"].Calls != 7 {
		t.Fatalf("telegram RPC metrics = %+v", decoded.TelegramRPC)
	}
	if decoded.DownloadTransport.Files != 1 || decoded.DownloadTransport.PeakThreads != 2 ||
		decoded.DownloadTransport.ThroughputMiBPerS != 4 || decoded.DownloadTransport.QueueWaitSeconds != 0.75 {
		t.Fatalf("download transport = %+v", decoded.DownloadTransport)
	}
	if decoded.TelegramBreakdown.GetDialogsCalls != 5 || decoded.TelegramBreakdown.GetDialogsWallSeconds != 3 ||
		decoded.TelegramBreakdown.GetDialogsWaitSeconds != 2.5 || decoded.TelegramBreakdown.GetHistoryCalls != 7 ||
		decoded.TelegramBreakdown.GetHistoryWallSeconds != 5 || decoded.TelegramBreakdown.GetHistoryWaitSeconds != 3.75 ||
		decoded.TelegramBreakdown.RPCServiceSeconds != 1.75 || decoded.TelegramBreakdown.DownloadQueueWaitSeconds != 0.75 ||
		decoded.TelegramBreakdown.DownloadTransferSeconds != 2 {
		t.Fatalf("telegram breakdown = %+v", decoded.TelegramBreakdown)
	}
	if decoded.StageWorkSeconds <= 0 {
		t.Fatalf("stage work = %f, want positive work total", decoded.StageWorkSeconds)
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

func TestDailyStageTimingReportAggregatesDownloadTransport(t *testing.T) {
	collector := newDailyStageTimingCollector("daily", "2026-07-30", "2026-07-31")
	collector.ObserveDownloadQueueWait(250 * time.Millisecond)
	collector.ObserveDownloadQueueWait(750 * time.Millisecond)
	collector.ObserveDownloadTransfer(stages.DownloadTransferMetrics{
		Policy:           "size-tiered-cap2",
		ExpectedBytes:    512 << 10,
		TransferredBytes: 512 << 10,
		Threads:          1,
		Seconds:          1,
	})
	collector.ObserveDownloadTransfer(stages.DownloadTransferMetrics{
		Policy:           "size-tiered-cap2",
		ExpectedBytes:    8 << 20,
		TransferredBytes: 7 << 20,
		Threads:          2,
		Seconds:          2,
		Retries:          2,
		FloodWaits:       1,
		TransportFloods:  1,
		Failed:           true,
	})

	metrics := collector.Report(nil).DownloadTransport
	if metrics.Policy != "size-tiered-cap2" || metrics.Files != 2 || metrics.Failed != 1 {
		t.Fatalf("download summary = %+v", metrics)
	}
	if metrics.ExpectedBytes != (8<<20)+(512<<10) || metrics.TransferredBytes != (7<<20)+(512<<10) {
		t.Fatalf("download bytes = %+v", metrics)
	}
	if metrics.Seconds != 3 || metrics.QueueWaitSeconds != 1 || metrics.PeakThreads != 2 || metrics.ThreadDecisions["1"] != 1 || metrics.ThreadDecisions["2"] != 1 {
		t.Fatalf("download decisions = %+v", metrics)
	}
	if metrics.Retries != 2 || metrics.DownloaderFloods != 1 || metrics.DownloaderTransportFloods != 1 || metrics.ThroughputMiBPerS != 2.5 {
		t.Fatalf("download transport evidence = %+v", metrics)
	}
}

func TestFinishDailyStageTimingsPrintsMetricsAndReportPath(t *testing.T) {
	stateDir := t.TempDir()
	collector := newDailyStageTimingCollector("daily", "2026-07-29", "2026-07-29")
	var output strings.Builder
	if err := finishDailyStageTimings(stateDir, collector, nil, &output); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"telegram_scan=", "download=", "ffmpeg=", "model_cold_start=", "asr=", "render=",
		"audio=", "asr_speed=", "pipeline_speed=", "checkpoint_enabled=", "checkpoint_history_dialogs=",
		"checkpoint_unchanged=", "checkpoint_changed=", "checkpoint_new=", "checkpoint_fallback=",
		"stage_work=", "pipeline_mode=", "pipeline_span=", "pipeline_overlap=", "pipeline_workers=", "pipeline_queue_peak=", "total=", "report=",
		"download_files=", "download_bytes=", "download_mib_s=", "download_peak_threads=", "download_retries=", "download_floods=", "download_transport_floods=", "download_queue_wait=", "download_transfer=",
		"rpc_spacing_ms=", "rpc_calls=", "rpc_wait=", "rpc_service=", "get_dialogs_calls=", "get_dialogs_wall=", "get_dialogs_wait=", "get_history_calls=", "get_history_wall=", "get_history_wait=", "transport_floods=", "history_data_pages=", "history_empty_proof_pages=", "history_sparse_continuations=",
		"checkpoint_proof_candidates=", "checkpoint_proof_stops=", "checkpoint_proof_shadow_confirmed=", "checkpoint_proof_shadow_rejected=",
	} {
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

func TestDailyStageTimingCollectorCopiesRPCMaps(t *testing.T) {
	operations := map[string]int{"get_history": 1}
	timings := map[string]harvest.RPCOperationTiming{
		"get_history": {Calls: 1, WallSeconds: 2, WaitSeconds: 1, ServiceSeconds: 1},
	}
	collector := newDailyStageTimingCollector("daily", "2026-07-31", "2026-07-31")
	collector.ObserveDownloadTransfer(stages.DownloadTransferMetrics{Threads: 1})
	collector.ObserveMediaPipeline(stages.MediaPipelineMetrics{Workers: []stages.MediaWorkerMetrics{{ID: 1, Jobs: 2}}})
	collector.ObserveOutgoingStats(harvest.OutgoingStats{RPCPacing: harvest.RPCPacingStats{
		Operations:       operations,
		OperationTimings: timings,
		ServiceSeconds:   1,
	}, CheckpointProofRejections: map[string]int{"head_mismatch": 1}})

	operations["get_history"] = 99
	timings["get_history"] = harvest.RPCOperationTiming{Calls: 99}
	report := collector.Report(nil)
	if report.TelegramRPC.Operations["get_history"] != 1 || report.TelegramRPC.OperationTimings["get_history"].Calls != 1 {
		t.Fatalf("collector retained caller-owned maps: %+v", report.TelegramRPC)
	}

	report.TelegramRPC.Operations["get_history"] = 77
	report.TelegramRPC.OperationTimings["get_history"] = harvest.RPCOperationTiming{Calls: 77}
	report.DownloadTransport.ThreadDecisions["1"] = 77
	report.HistoryPagination.ProofRejections["head_mismatch"] = 77
	report.MediaPipeline.Workers[0].Jobs = 77
	second := collector.Report(nil)
	if second.TelegramRPC.Operations["get_history"] != 1 || second.TelegramRPC.OperationTimings["get_history"].Calls != 1 {
		t.Fatalf("report exposed collector-owned maps: %+v", second.TelegramRPC)
	}
	if second.DownloadTransport.ThreadDecisions["1"] != 1 || second.HistoryPagination.ProofRejections["head_mismatch"] != 1 || second.MediaPipeline.Workers[0].Jobs != 2 {
		t.Fatalf("report exposed collector-owned aggregate state: download=%+v pagination=%+v pipeline=%+v", second.DownloadTransport, second.HistoryPagination, second.MediaPipeline)
	}
}

func TestDailyStageTimingReportLeavesSpeedZeroWithoutProcessedAudio(t *testing.T) {
	collector := newDailyStageTimingCollector("daily", "2026-07-29", "2026-07-29")
	collector.Observe(stages.ASR, 2*time.Second)
	report := collector.Report(nil)
	if report.AudioSeconds != 0 || report.ASRSpeedX != 0 || report.PipelineSpeedX != 0 {
		t.Fatalf("unexpected empty-run ASR metrics: %+v", report)
	}
}

func TestDailyStageTimingReportUsesOverlappingPipelineMetrics(t *testing.T) {
	collector := newDailyStageTimingCollector("daily", "2026-07-29", "2026-07-29")
	collector.Observe(stages.TelegramScan, 10*time.Second)
	collector.Observe(stages.ASR, 8*time.Second)
	collector.ObserveAudioDuration(40)
	collector.ObserveMediaPipeline(stages.MediaPipelineMetrics{
		Mode:             "single-gpu",
		WorkersRequested: 1,
		WorkersPeak:      1,
		SpanSeconds:      10,
		OverlapSeconds:   4,
		PoolSpeedX:       4,
		WorkerWorkSpeedX: 5,
	})
	collector.startedAt = time.Now().UTC().Add(-12 * time.Second)
	report := collector.Report(nil)
	if report.MediaPipeline == nil || report.MediaPipeline.WorkersPeak != 1 {
		t.Fatalf("pipeline = %+v", report.MediaPipeline)
	}
	if report.ASRSpeedX != 5 || report.PipelineSpeedX != 4 {
		t.Fatalf("speeds = asr %.2f pipeline %.2f", report.ASRSpeedX, report.PipelineSpeedX)
	}
	if report.StageWorkSeconds != 18 {
		t.Fatalf("stage work = %.2f, want 18 worker-seconds", report.StageWorkSeconds)
	}
	if report.TotalSeconds >= report.StageWorkSeconds {
		t.Fatalf("expected overlapping work seconds to exceed wall: total=%.2f work=%.2f", report.TotalSeconds, report.StageWorkSeconds)
	}
}
