package stages

import "time"

type Name string

const (
	TelegramScan Name = "telegram_scan"
	Download     Name = "download"
	FFmpeg       Name = "ffmpeg"
	Vosk         Name = "vosk"
	Render       Name = "render"
)

type Observer func(Name, time.Duration)

func ObserveSince(observer Observer, name Name, startedAt time.Time) {
	if observer == nil || startedAt.IsZero() {
		return
	}
	observer(name, time.Since(startedAt))
}
