package main

import (
	"flag"
	"fmt"

	"github.com/chupakobra6/telegram-harvest/internal/config"
	"github.com/chupakobra6/telegram-harvest/internal/harvest"
)

type syncTranscriptionFlags struct {
	enabled       *bool
	retain        *bool
	videoMode     *string
	transcriptDir *string
}

func addSyncTranscriptionFlags(fs *flag.FlagSet) syncTranscriptionFlags {
	return syncTranscriptionFlags{
		enabled:       fs.Bool("transcribe", false, "transcribe voice/audio/video and reuse cached transcripts (main with --download-media)"),
		retain:        fs.Bool("retain-transcription-media", false, "keep the original transcription media in media-dir"),
		videoMode:     fs.String("transcribe-video", harvest.VideoTranscribePhone, "generic video transcription mode: phone, all, or off"),
		transcriptDir: fs.String("transcript-dir", "transcripts", "transcript output directory, relative to state dir unless absolute"),
	}
}

func (flags syncTranscriptionFlags) apply(cfg config.Config, history *harvest.HistoryOptions) error {
	if *flags.retain && (!*flags.enabled || !history.DownloadMedia) {
		return fmt.Errorf("--retain-transcription-media requires --download-media and --transcribe")
	}
	if err := validateDailyOptions(dailyOptions{VideoTranscribeMode: *flags.videoMode}); err != nil {
		return err
	}
	if !*flags.enabled {
		return nil
	}
	if cfg.Mode != config.ModeMain {
		return fmt.Errorf("sync --transcribe is supported only for profile main")
	}
	if !history.DownloadMedia {
		return fmt.Errorf("--transcribe requires --download-media")
	}
	runtime := dailyRuntimeDefaults()
	history.TranscribeMedia = true
	history.KeepTranscribedMedia = *flags.retain
	history.VideoTranscribeMode = *flags.videoMode
	history.TranscriptDir = resolveOutputPath(cfg.StateDir, *flags.transcriptDir)
	history.WhisperCommand = runtime.WhisperCommand
	history.WhisperModelPath = runtime.WhisperModelPath
	history.WhisperGateFilePath = runtime.WhisperGateFilePath
	history.FFmpegCommand = runtime.FFmpegCommand
	if err := dailyTranscribeOptions(*history).Validate(); err != nil {
		return fmt.Errorf("--transcribe requires a configured transcriber: %w", err)
	}
	return nil
}
