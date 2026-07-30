package stages

import "time"

type Name string

const (
	TelegramScan   Name = "telegram_scan"
	Download       Name = "download"
	FFmpeg         Name = "ffmpeg"
	ModelColdStart Name = "model_cold_start"
	Vosk           Name = "vosk"
	Render         Name = "render"
)

type Observer func(Name, time.Duration)
type AudioDurationObserver func(float64)
type MediaPipelineObserver func(MediaPipelineMetrics)

type MediaPipelineMetrics struct {
	Mode                    string               `json:"mode,omitempty"`
	QueueCapacity           int                  `json:"queue_capacity"`
	QueuePeak               int                  `json:"queue_peak"`
	WorkersRequested        int                  `json:"workers_requested"`
	WorkersActivated        int                  `json:"workers_activated"`
	WorkersPeak             int                  `json:"workers_peak"`
	JobsSubmitted           int                  `json:"jobs_submitted"`
	JobsDeduplicated        int                  `json:"jobs_deduplicated"`
	JobsCompleted           int                  `json:"jobs_completed"`
	JobsFailed              int                  `json:"jobs_failed"`
	AudioSeconds            float64              `json:"audio_seconds"`
	SpanSeconds             float64              `json:"span_seconds"`
	OverlapSeconds          float64              `json:"overlap_seconds"`
	PoolSpeedX              float64              `json:"pool_speed_x"`
	WorkerWorkSpeedX        float64              `json:"worker_work_speed_x"`
	AvailableMemoryBytes    uint64               `json:"available_memory_bytes,omitempty"`
	CPUUtilization          float64              `json:"cpu_utilization,omitempty"`
	EstimatedWorkerRSSBytes uint64               `json:"estimated_worker_rss_bytes,omitempty"`
	ScaleDecisions          []MediaScaleDecision `json:"scale_decisions,omitempty"`
	Workers                 []MediaWorkerMetrics `json:"workers,omitempty"`
}

type MediaScaleDecision struct {
	At              time.Time `json:"at"`
	Workers         int       `json:"workers"`
	RemainingAudio  float64   `json:"remaining_audio_seconds"`
	ExpectedSaving  float64   `json:"expected_saving_seconds,omitempty"`
	AvailableMemory uint64    `json:"available_memory_bytes,omitempty"`
	CPUUtilization  float64   `json:"cpu_utilization,omitempty"`
	Action          string    `json:"action"`
	Reason          string    `json:"reason,omitempty"`
}

type MediaWorkerMetrics struct {
	ID                    int     `json:"id"`
	Jobs                  int     `json:"jobs"`
	Failures              int     `json:"failures"`
	AudioSeconds          float64 `json:"audio_seconds"`
	FFmpegSeconds         float64 `json:"ffmpeg_seconds"`
	ModelColdStartSeconds float64 `json:"model_cold_start_seconds"`
	VoskSeconds           float64 `json:"vosk_seconds"`
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
