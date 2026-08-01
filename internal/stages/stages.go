package stages

import "time"

type Name string

const (
	TelegramScan   Name = "telegram_scan"
	Download       Name = "download"
	FFmpeg         Name = "ffmpeg"
	ModelColdStart Name = "model_cold_start"
	ASR            Name = "asr"
	Render         Name = "render"
)

type Observer func(Name, time.Duration)
type AudioDurationObserver func(float64)
type MediaPipelineObserver func(MediaPipelineMetrics)
type DownloadTransferObserver func(DownloadTransferMetrics)
type DownloadQueueWaitObserver func(time.Duration)

type DownloadTransferMetrics struct {
	Policy           string                      `json:"policy,omitempty"`
	ExpectedBytes    int64                       `json:"expected_bytes,omitempty"`
	TransferredBytes int64                       `json:"transferred_bytes,omitempty"`
	Threads          int                         `json:"threads"`
	Seconds          float64                     `json:"seconds"`
	Retries          int                         `json:"retries"`
	FloodWaits       int                         `json:"flood_waits"`
	TransportFloods  int                         `json:"transport_floods"`
	Failed           bool                        `json:"failed"`
	Coordinator      *DownloadCoordinatorMetrics `json:"-"`
}

type DownloadCoordinatorMetrics struct {
	Policy                 string  `json:"policy,omitempty"`
	CapacitySlots          int     `json:"capacity_slots"`
	ActiveSlots            int     `json:"active_slots"`
	PeakActiveSlots        int     `json:"peak_active_slots"`
	PeakActiveFiles        int     `json:"peak_active_files"`
	Batches                int     `json:"batches"`
	Jobs                   int     `json:"jobs"`
	SmallJobs              int     `json:"small_jobs"`
	LargeJobs              int     `json:"large_jobs"`
	SmallParallelPairs     int     `json:"small_parallel_pairs"`
	QueueWaitSeconds       float64 `json:"queue_wait_seconds"`
	WallSeconds            float64 `json:"wall_seconds"`
	HistorySections        int     `json:"history_sections"`
	HistoryDownloadOverlap int     `json:"history_download_overlap"`
}

type DownloadTransportMetrics struct {
	Policy                    string                     `json:"policy,omitempty"`
	Files                     int                        `json:"files"`
	Failed                    int                        `json:"failed"`
	ExpectedBytes             int64                      `json:"expected_bytes"`
	TransferredBytes          int64                      `json:"transferred_bytes"`
	Seconds                   float64                    `json:"seconds"`
	QueueWaitSeconds          float64                    `json:"queue_wait_seconds"`
	ThroughputMiBPerS         float64                    `json:"throughput_mib_per_second"`
	PeakThreads               int                        `json:"peak_threads"`
	ThreadDecisions           map[string]int             `json:"thread_decisions,omitempty"`
	Retries                   int                        `json:"retries"`
	DownloaderFloods          int                        `json:"downloader_flood_waits"`
	DownloaderTransportFloods int                        `json:"downloader_transport_floods"`
	Coordinator               DownloadCoordinatorMetrics `json:"coordinator"`
}

func ObserveDownloadQueueWait(observer DownloadQueueWaitObserver, duration time.Duration) {
	if observer == nil || duration < 0 {
		return
	}
	observer(duration)
}

type MediaPipelineMetrics struct {
	Backend                 string               `json:"backend,omitempty"`
	Accelerator             string               `json:"accelerator,omitempty"`
	Model                   string               `json:"model,omitempty"`
	WorkerResource          string               `json:"worker_resource,omitempty"`
	Mode                    string               `json:"mode,omitempty"`
	QueueCapacity           int                  `json:"queue_capacity"`
	QueuePeak               int                  `json:"queue_peak"`
	WorkersRequested        int                  `json:"workers_requested"`
	WorkersActivated        int                  `json:"workers_activated"`
	WorkersPeak             int                  `json:"workers_peak"`
	JobsSubmitted           int                  `json:"jobs_submitted"`
	JobsDeduplicated        int                  `json:"jobs_deduplicated"`
	JobsCompleted           int                  `json:"jobs_completed"`
	JobsSkipped             int                  `json:"jobs_skipped"`
	JobsFailed              int                  `json:"jobs_failed"`
	AudioSeconds            float64              `json:"audio_seconds"`
	SpeechGateSeconds       float64              `json:"speech_gate_seconds,omitempty"`
	SpanSeconds             float64              `json:"span_seconds"`
	OverlapSeconds          float64              `json:"overlap_seconds"`
	PoolSpeedX              float64              `json:"pool_speed_x"`
	WorkerWorkSpeedX        float64              `json:"worker_work_speed_x"`
	AvailableMemoryBytes    uint64               `json:"available_memory_bytes,omitempty"`
	CPUUtilization          float64              `json:"cpu_utilization,omitempty"`
	SystemCPUMean           float64              `json:"system_cpu_mean,omitempty"`
	SystemCPUPeak           float64              `json:"system_cpu_peak,omitempty"`
	GPUUtilization          float64              `json:"gpu_utilization,omitempty"`
	GPUUtilizationAvailable bool                 `json:"gpu_utilization_available"`
	GPUUtilizationReason    string               `json:"gpu_utilization_reason,omitempty"`
	EstimatedWorkerRSSBytes uint64               `json:"estimated_worker_rss_bytes,omitempty"`
	Workers                 []MediaWorkerMetrics `json:"workers,omitempty"`
}

type MediaWorkerMetrics struct {
	ID                    int     `json:"id"`
	Jobs                  int     `json:"jobs"`
	Skips                 int     `json:"skips"`
	Failures              int     `json:"failures"`
	AudioSeconds          float64 `json:"audio_seconds"`
	FFmpegSeconds         float64 `json:"ffmpeg_seconds"`
	SpeechGateSeconds     float64 `json:"speech_gate_seconds,omitempty"`
	ModelColdStartSeconds float64 `json:"model_cold_start_seconds"`
	ASRSeconds            float64 `json:"asr_seconds"`
	BusySeconds           float64 `json:"busy_seconds"`
	ASRSpeedX             float64 `json:"asr_speed_x"`
	PeakRSSBytes          uint64  `json:"peak_rss_bytes,omitempty"`
}

func ObserveSince(observer Observer, name Name, startedAt time.Time) {
	if observer == nil || startedAt.IsZero() {
		return
	}
	observer(name, time.Since(startedAt))
}

func ObserveAudioDuration(observer AudioDurationObserver, seconds float64) {
	if observer == nil || seconds <= 0 {
		return
	}
	observer(seconds)
}
