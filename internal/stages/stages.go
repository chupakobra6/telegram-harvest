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
