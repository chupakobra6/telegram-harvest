package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/stages"
)

const whisperReadyTimeout = 2 * time.Minute
const whisperTerminalHallucinationProfile = "terminal-exact-v1"

// WhisperServerRunner owns one long-lived whisper-server process. The model is
// loaded exactly once per activated worker and inference remains serialized.
type WhisperServerRunner struct {
	opts Options

	mu        sync.Mutex
	cmd       *exec.Cmd
	baseURL   string
	stderr    *synchronizedBuffer
	stdout    *synchronizedBuffer
	waitDone  chan error
	client    *http.Client
	closed    bool
	processID atomic.Int64
}

type whisperResponse struct {
	Text     string           `json:"text"`
	Error    string           `json:"error"`
	Language string           `json:"language,omitempty"`
	Duration float64          `json:"duration,omitempty"`
	Segments []whisperSegment `json:"segments,omitempty"`
}

type whisperSegment struct {
	Start                 *float64 `json:"start,omitempty"`
	End                   *float64 `json:"end,omitempty"`
	AverageLogProbability float64  `json:"avg_logprob"`
	NoSpeechProbability   float64  `json:"no_speech_prob"`
}

type whisperRequestOptions struct {
	SegmentTimestamps  bool
	Language           string
	InitialPrompt      string
	CarryInitialPrompt bool
	DetectLanguage     bool
}

func (r *WhisperServerRunner) Run(ctx context.Context, inputPath string, outputPath string) (string, error) {
	result, err := r.RunDetailed(ctx, inputPath, outputPath)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

func (r *WhisperServerRunner) RunDetailed(ctx context.Context, inputPath string, outputPath string) (Result, error) {
	start := time.Now()
	if strings.TrimSpace(inputPath) == "" {
		return Result{}, fmt.Errorf("input path is empty")
	}
	if strings.TrimSpace(outputPath) == "" {
		return Result{}, fmt.Errorf("output path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return Result{}, fmt.Errorf("prepare transcript dir: %w", err)
	}

	ffmpegStart := time.Now()
	wavPath, cleanup, err := convertToASRWAV(ctx, r.opts, inputPath, outputPath)
	stages.ObserveSince(r.opts.StageTiming, stages.FFmpeg, ffmpegStart)
	if err != nil {
		return Result{}, err
	}
	ffmpegDuration := time.Since(ffmpegStart)
	defer cleanup()
	wavBytes := fileSize(wavPath)
	diagnostics := &Diagnostics{}
	var longFormPreparationDuration time.Duration
	var leadingSpeechOffset float64
	var lastDetectedSpeechEnd float64
	if r.opts.WhisperLongForm.Enabled {
		preparationStart := time.Now()
		prepared, preparationErr := prepareLongFormWAV(ctx, r.opts, wavPath, filepath.Dir(outputPath))
		longFormPreparationDuration = time.Since(preparationStart)
		if preparationErr != nil {
			return Result{}, preparationErr
		}
		defer prepared.cleanup()
		wavPath = prepared.path
		wavBytes = fileSize(wavPath)
		leadingSpeechOffset = prepared.leadingSpeechOffset
		lastDetectedSpeechEnd = prepared.lastSpeechEnd
		diagnostics.LeadingSpeechOffsetSeconds = leadingSpeechOffset
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Result{}, fmt.Errorf("whisper.cpp session is closed")
	}
	var speechGateDuration time.Duration
	if r.opts.WhisperSpeechGate.Enabled {
		gateStart := time.Now()
		hasSpeech, gateErr := runWhisperSpeechGate(ctx, r.opts, wavPath)
		speechGateDuration = time.Since(gateStart)
		if r.opts.StageTiming != nil {
			r.opts.StageTiming(stages.ASR, speechGateDuration)
		}
		diagnostics.SpeechGatePassed = &hasSpeech
		if gateErr != nil {
			return Result{}, gateErr
		}
		if !hasSpeech {
			if err := os.WriteFile(outputPath, nil, 0o600); err != nil {
				return Result{}, fmt.Errorf("write empty speech-gated transcript: %w", err)
			}
			return Result{
				Engine:             r.opts.EngineName(),
				Backend:            r.opts.Descriptor(),
				FFmpegDuration:     ffmpegDuration,
				ASRDuration:        speechGateDuration,
				SpeechGateDuration: speechGateDuration,
				TotalDuration:      time.Since(start),
				InputBytes:         fileSize(inputPath),
				WAVBytes:           wavBytes,
				WAVDurationSeconds: wavPCM16MonoDuration(wavBytes),
				Diagnostics:        diagnostics,
			}, nil
		}
	}
	modelColdStartDuration, err := r.startLocked(ctx)
	if modelColdStartDuration > 0 && r.opts.StageTiming != nil {
		r.opts.StageTiming(stages.ModelColdStart, modelColdStartDuration)
	}
	if err != nil {
		return Result{}, err
	}
	inferenceStart := time.Now()
	var text string
	var inferenceDiagnostics *Diagnostics
	if r.opts.WhisperLongForm.Enabled {
		text, inferenceDiagnostics, err = r.inferLongFormLocked(ctx, wavPath, lastDetectedSpeechEnd)
	} else {
		text, inferenceDiagnostics, err = r.inferLocked(ctx, wavPath)
	}
	inferenceDuration := time.Since(inferenceStart)
	if r.opts.StageTiming != nil {
		r.opts.StageTiming(stages.ASR, inferenceDuration)
	}
	if err != nil {
		return Result{}, err
	}
	mergeDiagnostics(diagnostics, inferenceDiagnostics)
	languageDetectionDuration := time.Duration(diagnostics.LanguageDetectionSeconds * float64(time.Second))
	asrDuration := speechGateDuration + inferenceDuration
	text = strings.TrimSpace(text)
	var removed []string
	text, removed = stripWhisperTerminalHallucinations(text)
	diagnostics.RemovedTerminalHallucinations = append(diagnostics.RemovedTerminalHallucinations, removed...)
	if err := os.WriteFile(outputPath, []byte(text), 0o600); err != nil {
		return Result{}, fmt.Errorf("write transcript: %w", err)
	}
	return Result{
		Text:                        text,
		Engine:                      r.opts.EngineName(),
		Backend:                     r.opts.Descriptor(),
		FFmpegDuration:              ffmpegDuration,
		ModelColdStartDuration:      modelColdStartDuration,
		ASRDuration:                 asrDuration,
		SpeechGateDuration:          speechGateDuration,
		LongFormPreparationDuration: longFormPreparationDuration,
		LanguageDetectionDuration:   languageDetectionDuration,
		LeadingSpeechOffset:         leadingSpeechOffset,
		TotalDuration:               time.Since(start),
		InputBytes:                  fileSize(inputPath),
		WAVBytes:                    wavBytes,
		WAVDurationSeconds:          wavPCM16MonoDuration(wavBytes),
		TranscriptBytes:             int64(len([]byte(text))),
		Diagnostics:                 diagnostics,
	}, nil
}

func (r *WhisperServerRunner) inferLongFormLocked(ctx context.Context, wavPath string, lastDetectedSpeechEnd float64) (string, *Diagnostics, error) {
	duration := wavPCM16MonoDuration(fileSize(wavPath))
	if duration <= 0 {
		return "", nil, fmt.Errorf("trusted long-form inference received empty audio")
	}
	settings := r.opts.WhisperLongForm.normalized()
	detectionStart := time.Now()
	probePath, cleanupProbe, err := prepareLanguageProbeWAV(ctx, r.opts, wavPath, settings.LanguageProbeSeconds)
	if err != nil {
		return "", nil, err
	}
	detected, err := r.inferRequestLocked(ctx, probePath, whisperRequestOptions{
		Language:       "auto",
		DetectLanguage: true,
	})
	cleanupProbe()
	if err != nil {
		return "", nil, err
	}
	languageDetectionDuration := time.Since(detectionStart)
	decodeRequest, err := longFormDecodeRequest(settings, detected.Language)
	if err != nil {
		return "", nil, err
	}
	decoded, err := r.inferRequestLocked(ctx, wavPath, decodeRequest)
	if err != nil {
		return "", nil, err
	}
	coverageGap, err := validateTimestampedLongFormResponse(decoded, duration, lastDetectedSpeechEnd, settings.TrailingCoverageToleranceSeconds)
	if err != nil {
		return "", nil, err
	}
	diagnostics := whisperDiagnostics(decoded.Segments)
	diagnostics.TimestampedSegments = true
	diagnostics.DecodedAudioDurationSeconds = decoded.Duration
	diagnostics.FirstSegmentStartSeconds = *decoded.Segments[0].Start
	diagnostics.LastSegmentEndSeconds = *decoded.Segments[len(decoded.Segments)-1].End
	diagnostics.LastDetectedSpeechEndSeconds = lastDetectedSpeechEnd
	diagnostics.TrailingSpeechCoverageGapSeconds = coverageGap
	diagnostics.TrailingCoverageToleranceSeconds = settings.TrailingCoverageToleranceSeconds
	diagnostics.CoverageValidated = true
	diagnostics.DetectedLanguage = strings.ToLower(strings.TrimSpace(detected.Language))
	diagnostics.LanguageDetectionSeconds = languageDetectionDuration.Seconds()
	diagnostics.InitialPromptApplied = decodeRequest.InitialPrompt != ""
	text, removed := stripWhisperLongFormTerminalRepetitions(decoded.Text)
	diagnostics.RemovedTerminalHallucinations = append(diagnostics.RemovedTerminalHallucinations, removed...)
	return text, diagnostics, nil
}

func prepareLanguageProbeWAV(ctx context.Context, opts Options, wavPath string, maxSeconds int) (string, func(), error) {
	duration := wavPCM16MonoDuration(fileSize(wavPath))
	if duration <= 0 {
		return "", func() {}, fmt.Errorf("trusted long-form language probe received empty audio")
	}
	if maxSeconds <= 0 {
		return "", func() {}, fmt.Errorf("trusted long-form language probe duration must be positive")
	}
	if duration <= float64(maxSeconds) {
		return wavPath, func() {}, nil
	}
	probePath, cleanup, err := temporaryWAV(filepath.Dir(wavPath), ".asr-language-probe-*.wav")
	if err != nil {
		return "", func() {}, err
	}
	if err := extractASRWAVRange(ctx, opts, wavPath, probePath, 0, float64(maxSeconds)); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("prepare trusted long-form language probe: %w", err)
	}
	return probePath, cleanup, nil
}

func longFormDecodeRequest(settings WhisperLongFormDescriptor, detectedLanguage string) (whisperRequestOptions, error) {
	detectedLanguage = strings.ToLower(strings.TrimSpace(detectedLanguage))
	request := whisperRequestOptions{SegmentTimestamps: true}
	switch detectedLanguage {
	case "russian", "ru":
		request.Language = "ru"
		request.InitialPrompt = settings.RussianInitialPrompt
		request.CarryInitialPrompt = settings.CarryInitialPrompt
	case "english", "en":
		request.Language = "en"
	case "":
		return whisperRequestOptions{}, fmt.Errorf("whisper.cpp language probe returned no language")
	default:
		request.Language = "auto"
	}
	return request, nil
}

func validateTimestampedLongFormResponse(decoded whisperResponse, expectedDuration, lastDetectedSpeechEnd, coverageTolerance float64) (float64, error) {
	if decoded.Duration <= 0 || math.Abs(decoded.Duration-expectedDuration) > 1 {
		return 0, fmt.Errorf("whisper.cpp long-form duration %.3fs does not match input %.3fs", decoded.Duration, expectedDuration)
	}
	if strings.TrimSpace(decoded.Text) == "" || len(decoded.Segments) == 0 {
		return 0, fmt.Errorf("whisper.cpp long-form returned no timestamped speech")
	}
	if lastDetectedSpeechEnd <= 0 || lastDetectedSpeechEnd > expectedDuration+1 {
		return 0, fmt.Errorf("trusted long-form VAD returned invalid last speech end %.3fs for %.3fs input", lastDetectedSpeechEnd, expectedDuration)
	}
	if coverageTolerance <= 0 {
		return 0, fmt.Errorf("trusted long-form coverage tolerance must be positive")
	}
	previousStart := -1.0
	previousEnd := -1.0
	for index, segment := range decoded.Segments {
		if segment.Start == nil || segment.End == nil {
			return 0, fmt.Errorf("whisper.cpp long-form segment %d has no timestamps", index)
		}
		start := *segment.Start
		end := *segment.End
		if start < 0 || end < start || start < previousStart || end < previousEnd || end > decoded.Duration+1 {
			return 0, fmt.Errorf("whisper.cpp long-form segment %d has invalid timestamps %.3f..%.3f", index, start, end)
		}
		previousStart = start
		previousEnd = end
	}
	coverageGap := math.Max(0, lastDetectedSpeechEnd-previousEnd)
	if coverageGap > coverageTolerance {
		return coverageGap, fmt.Errorf("whisper.cpp long-form ended at %.3fs, %.3fs before the last detected speech at %.3fs (tolerance %.3fs)", previousEnd, coverageGap, lastDetectedSpeechEnd, coverageTolerance)
	}
	return coverageGap, nil
}

type longFormAudioPreparation struct {
	path                string
	leadingSpeechOffset float64
	lastSpeechEnd       float64
	cleanup             func()
}

type vadSpeechSegment struct {
	start float64
	end   float64
}

var (
	vadSpeechCountPattern   = regexp.MustCompile(`(?m)^Detected (\d+) speech segments:\r?$`)
	vadSpeechSegmentPattern = regexp.MustCompile(`(?m)^Speech segment \d+: start = ([0-9]+(?:\.[0-9]+)?), end = ([0-9]+(?:\.[0-9]+)?)\r?$`)
)

func prepareLongFormWAV(ctx context.Context, opts Options, wavPath, tempDir string) (longFormAudioPreparation, error) {
	duration := wavPCM16MonoDuration(fileSize(wavPath))
	if duration <= 0 {
		return longFormAudioPreparation{}, fmt.Errorf("trusted long-form preparation received empty audio")
	}
	settings := opts.WhisperLongForm.normalized()
	firstSpeech, lastSpeechInFirstWindow, firstWindowCoversEnd, err := findFirstSpeech(ctx, opts, wavPath, tempDir, duration)
	if err != nil {
		return longFormAudioPreparation{}, err
	}
	lastSpeechEnd := lastSpeechInFirstWindow
	if !firstWindowCoversEnd {
		lastSpeechEnd, err = findLastSpeech(ctx, opts, wavPath, tempDir, duration)
		if err != nil {
			return longFormAudioPreparation{}, err
		}
	}
	trimOffset := math.Max(0, firstSpeech-float64(settings.LeadInMS)/1000)
	relativeLastSpeechEnd := lastSpeechEnd - trimOffset
	if relativeLastSpeechEnd <= 0 {
		return longFormAudioPreparation{}, fmt.Errorf("trusted long-form speech bounds %.3f..%.3f are invalid", firstSpeech, lastSpeechEnd)
	}
	if trimOffset < 0.001 {
		return longFormAudioPreparation{
			path:          wavPath,
			lastSpeechEnd: relativeLastSpeechEnd,
			cleanup:       func() {},
		}, nil
	}
	trimmedPath, cleanup, err := temporaryWAV(tempDir, ".asr-leading-trimmed-*.wav")
	if err != nil {
		return longFormAudioPreparation{}, err
	}
	if err := extractASRWAVRange(ctx, opts, wavPath, trimmedPath, trimOffset, duration-trimOffset); err != nil {
		cleanup()
		return longFormAudioPreparation{}, err
	}
	return longFormAudioPreparation{
		path:                trimmedPath,
		leadingSpeechOffset: trimOffset,
		lastSpeechEnd:       relativeLastSpeechEnd,
		cleanup:             cleanup,
	}, nil
}

func findFirstSpeech(ctx context.Context, opts Options, wavPath, tempDir string, duration float64) (float64, float64, bool, error) {
	settings := opts.WhisperLongForm.normalized()
	window := float64(settings.ScanWindowSeconds)
	step := float64(settings.ScanWindowSeconds - settings.ScanOverlapSeconds)
	for offset := 0.0; offset < duration; offset += step {
		windowDuration := math.Min(window, duration-offset)
		segments, err := probeSpeechWindow(ctx, opts, wavPath, tempDir, offset, windowDuration, ".asr-leading-window-*.wav")
		if err != nil {
			return 0, 0, false, err
		}
		if len(segments) > 0 {
			first := offset + segments[0].start
			last := offset + segments[len(segments)-1].end
			return first, last, offset+windowDuration >= duration, nil
		}
		if offset+windowDuration >= duration {
			break
		}
	}
	return 0, 0, false, fmt.Errorf("trusted long-form preparation found no speech in %.3fs", duration)
}

func findLastSpeech(ctx context.Context, opts Options, wavPath, tempDir string, duration float64) (float64, error) {
	settings := opts.WhisperLongForm.normalized()
	window := float64(settings.ScanWindowSeconds)
	overlap := float64(settings.ScanOverlapSeconds)
	windowEnd := duration
	for windowEnd > 0 {
		offset := math.Max(0, windowEnd-window)
		windowDuration := windowEnd - offset
		segments, err := probeSpeechWindow(ctx, opts, wavPath, tempDir, offset, windowDuration, ".asr-trailing-window-*.wav")
		if err != nil {
			return 0, err
		}
		if len(segments) > 0 {
			return offset + segments[len(segments)-1].end, nil
		}
		if offset == 0 {
			break
		}
		windowEnd = offset + overlap
	}
	return 0, fmt.Errorf("trusted long-form trailing scan found no speech in %.3fs", duration)
}

func probeSpeechWindow(ctx context.Context, opts Options, wavPath, tempDir string, offset, duration float64, pattern string) ([]vadSpeechSegment, error) {
	chunkPath, cleanup, err := temporaryWAV(tempDir, pattern)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if err := extractASRWAVRange(ctx, opts, wavPath, chunkPath, offset, duration); err != nil {
		return nil, err
	}
	segments, err := runSpeechProbe(ctx, opts, chunkPath)
	if err != nil {
		return nil, err
	}
	if len(segments) > 0 && segments[len(segments)-1].end > duration+1 {
		return nil, fmt.Errorf("whisper speech-boundary probe ended at %.3fs beyond %.3fs window", segments[len(segments)-1].end, duration)
	}
	return segments, nil
}

func temporaryWAV(dir, pattern string) (string, func(), error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", func() {}, fmt.Errorf("create temporary ASR wav: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close temporary ASR wav: %w", err)
	}
	return path, cleanup, nil
}

func extractASRWAVRange(ctx context.Context, opts Options, inputPath, outputPath string, offset, duration float64) error {
	ffmpegCommand := strings.TrimSpace(opts.FFmpegCommand)
	if ffmpegCommand == "" {
		ffmpegCommand = DefaultFFmpegCommand
	}
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-ss", strconv.FormatFloat(offset, 'f', 3, 64),
		"-t", strconv.FormatFloat(duration, 'f', 3, 64),
		"-i", inputPath,
		"-ac", "1", "-ar", "16000", "-sample_fmt", "s16",
		outputPath,
	}
	if output, err := exec.CommandContext(ctx, ffmpegCommand, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("prepare ASR audio range: %w: %s", err, trimDetail(string(output)))
	}
	return nil
}

func runSpeechProbe(ctx context.Context, opts Options, wavPath string) ([]vadSpeechSegment, error) {
	settings := opts.WhisperLongForm.normalized()
	args := whisperVADArgs(
		opts.WhisperLongForm.ModelPath,
		settings.Threshold,
		settings.MinSilenceDurationMS,
		settings.MinSpeechDurationMS,
		settings.SpeechPadMS,
		wavPath,
	)
	command := exec.CommandContext(ctx, opts.WhisperLongForm.command(opts), args...)
	command.Env = commandEnvironment(opts.Environment)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("whisper speech-boundary probe: %w: %s", err, trimDetail(string(output)))
	}
	return parseVADSpeechSegments(string(output))
}

func parseVADSpeechSegments(output string) ([]vadSpeechSegment, error) {
	countMatch := vadSpeechCountPattern.FindStringSubmatch(output)
	if len(countMatch) != 2 {
		return nil, fmt.Errorf("whisper speech-boundary probe returned an unrecognized result: %s", trimDetail(output))
	}
	declaredCount, err := strconv.Atoi(countMatch[1])
	if err != nil {
		return nil, fmt.Errorf("parse speech segment count %q: %w", countMatch[1], err)
	}
	if declaredCount == 0 {
		return nil, nil
	}
	matches := vadSpeechSegmentPattern.FindAllStringSubmatch(output, -1)
	if len(matches) != declaredCount {
		return nil, fmt.Errorf("whisper speech-boundary probe declared %d segments but returned %d", declaredCount, len(matches))
	}
	segments := make([]vadSpeechSegment, 0, len(matches))
	previousStart := -1.0
	previousEnd := -1.0
	for index, match := range matches {
		startCentiseconds, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			return nil, fmt.Errorf("parse speech segment %d start %q: %w", index, match[1], err)
		}
		endCentiseconds, err := strconv.ParseFloat(match[2], 64)
		if err != nil {
			return nil, fmt.Errorf("parse speech segment %d end %q: %w", index, match[2], err)
		}
		segment := vadSpeechSegment{start: startCentiseconds / 100, end: endCentiseconds / 100}
		if segment.start < 0 || segment.end < segment.start || segment.start < previousStart || segment.end < previousEnd {
			return nil, fmt.Errorf("whisper speech-boundary probe returned invalid segment %d: %.3f..%.3f", index, segment.start, segment.end)
		}
		segments = append(segments, segment)
		previousStart = segment.start
		previousEnd = segment.end
	}
	return segments, nil
}

func (r *WhisperServerRunner) startLocked(ctx context.Context) (time.Duration, error) {
	if r.cmd != nil {
		return 0, nil
	}
	startedAt := time.Now()
	if err := r.opts.Validate(); err != nil {
		return time.Since(startedAt), err
	}
	port, err := availableLoopbackPort()
	if err != nil {
		return time.Since(startedAt), err
	}
	threads := normalizedThreads(r.opts.WhisperThreads)
	args := []string{
		"--model", strings.TrimSpace(r.opts.WhisperModelPath),
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
		"--threads", strconv.Itoa(threads),
		"--processors", "1",
		"--language", normalizedLanguage(r.opts.Language),
		"--no-timestamps",
		"--no-language-probabilities",
	}
	cmd := exec.Command(strings.TrimSpace(r.opts.WhisperCommand), args...)
	cmd.Env = commandEnvironment(r.opts.Environment)
	stdout := &synchronizedBuffer{}
	stderr := &synchronizedBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return time.Since(startedAt), fmt.Errorf("start whisper.cpp server: %w", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	r.cmd = cmd
	r.processID.Store(int64(cmd.Process.Pid))
	r.stdout = stdout
	r.stderr = stderr
	r.waitDone = waitDone
	r.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	r.client = &http.Client{Timeout: 0}

	readyCtx, cancel := context.WithTimeout(ctx, whisperReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, requestErr := http.NewRequestWithContext(readyCtx, http.MethodGet, r.baseURL+"/health", nil)
		if requestErr == nil {
			response, getErr := r.client.Do(request)
			if getErr == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					if err := r.verifyAccelerationLocked(); err != nil {
						r.stopProcessLocked(true)
						return time.Since(startedAt), err
					}
					return time.Since(startedAt), nil
				}
			}
		}
		select {
		case err := <-waitDone:
			r.waitDone = closedWaitChannel(err)
			r.stopProcessLocked(true)
			return time.Since(startedAt), fmt.Errorf("whisper.cpp server exited before readiness: %v%s", err, r.processDetailLocked())
		case <-readyCtx.Done():
			r.stopProcessLocked(true)
			return time.Since(startedAt), fmt.Errorf("wait for whisper.cpp readiness: %w%s", readyCtx.Err(), r.processDetailLocked())
		case <-ticker.C:
		}
	}
}

func (r *WhisperServerRunner) verifyAccelerationLocked() error {
	evidence := strings.ToLower(r.processOutputLocked())
	if !strings.Contains(evidence, "ggml_metal_init: found device") {
		return fmt.Errorf("whisper.cpp did not confirm Metal activation%s", r.processDetailLocked())
	}
	if strings.Contains(evidence, "core ml model loaded") || strings.Contains(evidence, "coreml = 1") {
		return fmt.Errorf("whisper.cpp unexpectedly activated Core ML; production requires Metal-only%s", r.processDetailLocked())
	}
	return nil
}

func (r *WhisperServerRunner) inferLocked(ctx context.Context, wavPath string) (string, *Diagnostics, error) {
	decoded, err := r.inferRequestLocked(ctx, wavPath, whisperRequestOptions{})
	if err != nil {
		return "", nil, err
	}
	return decoded.Text, whisperDiagnostics(decoded.Segments), nil
}

func (r *WhisperServerRunner) inferRequestLocked(ctx context.Context, wavPath string, requestOptions whisperRequestOptions) (whisperResponse, error) {
	file, err := os.Open(wavPath)
	if err != nil {
		return whisperResponse{}, fmt.Errorf("open whisper.cpp WAV: %w", err)
	}
	bodyReader, bodyWriter := io.Pipe()
	writer := multipart.NewWriter(bodyWriter)
	contentType := writer.FormDataContentType()
	requestLanguage := strings.TrimSpace(requestOptions.Language)
	if requestLanguage == "" {
		requestLanguage = normalizedLanguage(r.opts.Language)
	}
	go streamWhisperRequest(writer, bodyWriter, file, wavPath, requestLanguage, r.opts.WhisperDecode, requestOptions)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/inference", bodyReader)
	if err != nil {
		_ = bodyReader.Close()
		return whisperResponse{}, fmt.Errorf("prepare whisper.cpp request: %w", err)
	}
	request.Header.Set("Content-Type", contentType)
	response, err := r.client.Do(request)
	if err != nil {
		return whisperResponse{}, fmt.Errorf("whisper.cpp inference: %w%s", err, r.processDetailLocked())
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return whisperResponse{}, fmt.Errorf("read whisper.cpp response: %w", err)
	}
	var decoded whisperResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return whisperResponse{}, fmt.Errorf("decode whisper.cpp response: %w: %s", err, trimDetail(string(payload)))
	}
	if response.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(decoded.Error)
		if detail == "" {
			detail = trimDetail(string(payload))
		}
		return whisperResponse{}, fmt.Errorf("whisper.cpp inference status %s: %s", response.Status, detail)
	}
	if detail := strings.TrimSpace(decoded.Error); detail != "" {
		return whisperResponse{}, fmt.Errorf("whisper.cpp inference: %s", detail)
	}
	return decoded, nil
}

func streamWhisperRequest(
	writer *multipart.Writer,
	pipe *io.PipeWriter,
	file *os.File,
	wavPath string,
	language string,
	decode WhisperDecodeOptions,
	requestOptions whisperRequestOptions,
) {
	fail := func(err error) {
		_ = file.Close()
		_ = pipe.CloseWithError(err)
	}
	filePart, err := writer.CreateFormFile("file", filepath.Base(wavPath))
	if err != nil {
		fail(fmt.Errorf("prepare whisper.cpp upload: %w", err))
		return
	}
	if _, err := io.Copy(filePart, file); err != nil {
		fail(fmt.Errorf("read whisper.cpp WAV: %w", err))
		return
	}
	if err := file.Close(); err != nil {
		_ = pipe.CloseWithError(fmt.Errorf("close whisper.cpp WAV: %w", err))
		return
	}
	settings := decode.normalized()
	fields := map[string]string{
		"response_format":  "verbose_json",
		"language":         language,
		"temperature":      strconv.FormatFloat(settings.Temperature, 'f', -1, 64),
		"temperature_inc":  strconv.FormatFloat(settings.TemperatureIncrement, 'f', -1, 64),
		"best_of":          strconv.Itoa(settings.BestOf),
		"beam_size":        strconv.Itoa(settings.BeamSize),
		"no_speech_thold":  strconv.FormatFloat(settings.NoSpeechThreshold, 'f', -1, 64),
		"logprob_thold":    strconv.FormatFloat(settings.LogProbabilityThreshold, 'f', -1, 64),
		"entropy_thold":    strconv.FormatFloat(settings.EntropyThreshold, 'f', -1, 64),
		"suppress_nst":     strconv.FormatBool(settings.SuppressNonSpeechTokens),
		"no_timestamps":    strconv.FormatBool(!requestOptions.SegmentTimestamps),
		"token_timestamps": "false",
	}
	if requestOptions.DetectLanguage {
		fields["detect_language"] = "true"
	}
	if prompt := strings.TrimSpace(requestOptions.InitialPrompt); prompt != "" {
		fields["prompt"] = prompt
		fields["carry_initial_prompt"] = strconv.FormatBool(requestOptions.CarryInitialPrompt)
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			_ = pipe.CloseWithError(fmt.Errorf("prepare whisper.cpp field %s: %w", key, err))
			return
		}
	}
	if err := writer.Close(); err != nil {
		_ = pipe.CloseWithError(fmt.Errorf("finish whisper.cpp upload: %w", err))
		return
	}
	_ = pipe.Close()
}

func runWhisperSpeechGate(ctx context.Context, opts Options, wavPath string) (bool, error) {
	gate := opts.WhisperSpeechGate.normalized()
	args := whisperSpeechGateArgs(opts, gate, wavPath)
	command := exec.CommandContext(ctx, opts.WhisperSpeechGate.command(opts), args...)
	command.Env = commandEnvironment(opts.Environment)
	var stdout synchronizedBuffer
	var stderr synchronizedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return false, fmt.Errorf("whisper speech gate: %w: %s", err, trimDetail(stderr.String()))
	}
	output := stdout.String()
	switch {
	case strings.Contains(output, "Detected 0 speech segments:"):
		return false, nil
	case strings.Contains(output, "speech segments:"):
		return true, nil
	default:
		return false, fmt.Errorf("whisper speech gate returned an unrecognized result: %s", trimDetail(output))
	}
}

func whisperSpeechGateArgs(opts Options, gate WhisperSpeechGateDescriptor, wavPath string) []string {
	return whisperVADArgs(
		opts.WhisperSpeechGate.ModelPath,
		gate.Threshold,
		gate.MinSilenceDurationMS,
		gate.MinSpeechDurationMS,
		gate.SpeechPadMS,
		wavPath,
	)
}

func whisperVADArgs(modelPath string, threshold float64, minSilenceDurationMS, minSpeechDurationMS, speechPadMS int, wavPath string) []string {
	return []string{
		"--no-prints",
		"--vad-model", strings.TrimSpace(modelPath),
		"--vad-threshold", strconv.FormatFloat(threshold, 'f', -1, 64),
		// whisper.cpp v1.9.1 accidentally assigns the min-silence value to
		// min-speech. Sending min-speech second preserves both the intended
		// gate behavior on v1.9.1 and the correct behavior after upstream fixes.
		"--vad-min-silence-duration-ms", strconv.Itoa(minSilenceDurationMS),
		"--vad-min-speech-duration-ms", strconv.Itoa(minSpeechDurationMS),
		"--vad-speech-pad-ms", strconv.Itoa(speechPadMS),
		"--file", wavPath,
	}
}

func whisperDiagnostics(segments []whisperSegment) *Diagnostics {
	diagnostics := &Diagnostics{Segments: len(segments)}
	if len(segments) == 0 {
		return diagnostics
	}
	diagnostics.MinimumAverageLogProb = segments[0].AverageLogProbability
	for _, segment := range segments {
		diagnostics.MeanAverageLogProb += segment.AverageLogProbability
		diagnostics.MeanNoSpeechProb += segment.NoSpeechProbability
		if segment.AverageLogProbability < diagnostics.MinimumAverageLogProb {
			diagnostics.MinimumAverageLogProb = segment.AverageLogProbability
		}
		if segment.NoSpeechProbability > diagnostics.MaximumNoSpeechProb {
			diagnostics.MaximumNoSpeechProb = segment.NoSpeechProbability
		}
	}
	diagnostics.MeanAverageLogProb /= float64(len(segments))
	diagnostics.MeanNoSpeechProb /= float64(len(segments))
	return diagnostics
}

var whisperTerminalHallucinations = map[string]struct{}{
	"продолжение следует":          {},
	"субтитры сделал dimatorzok":   {},
	"субтитры создавал dimatorzok": {},
	"спасибо за просмотр":          {},
	"подпишись":                    {},
}

// stripWhisperLongFormTerminalRepetitions removes only consecutive identical
// lines at the very end of a timestamped long-form result. Repetition loops are
// a known terminal failure mode; keeping the first occurrence preserves a real
// closing word while avoiding a broad phrase blacklist.
func stripWhisperLongFormTerminalRepetitions(text string) (string, []string) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	var removed []string
	for len(lines) > 1 {
		last := strings.TrimSpace(lines[len(lines)-1])
		previous := strings.TrimSpace(lines[len(lines)-2])
		if last == "" || normalizeWhisperHallucination(last) != normalizeWhisperHallucination(previous) {
			break
		}
		removed = append(removed, last)
		lines = lines[:len(lines)-1]
	}
	for left, right := 0, len(removed)-1; left < right; left, right = left+1, right-1 {
		removed[left], removed[right] = removed[right], removed[left]
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), removed
}

func stripWhisperTerminalHallucinations(text string) (string, []string) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	var removed []string
	for len(lines) > 0 {
		last := strings.TrimSpace(lines[len(lines)-1])
		if _, known := whisperTerminalHallucinations[normalizeWhisperHallucination(last)]; !known {
			break
		}
		removed = append(removed, last)
		lines = lines[:len(lines)-1]
	}
	for left, right := 0, len(removed)-1; left < right; left, right = left+1, right-1 {
		removed[left], removed[right] = removed[right], removed[left]
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), removed
}

func normalizeWhisperHallucination(value string) string {
	value = strings.ToLower(strings.ReplaceAll(value, "ё", "е"))
	var normalized strings.Builder
	previousSpace := true
	for _, current := range value {
		isASCII := current >= 'a' && current <= 'z'
		isCyrillic := current >= 'а' && current <= 'я'
		isDigit := current >= '0' && current <= '9'
		if isASCII || isCyrillic || isDigit {
			normalized.WriteRune(current)
			previousSpace = false
			continue
		}
		if !previousSpace {
			normalized.WriteByte(' ')
			previousSpace = true
		}
	}
	return strings.TrimSpace(normalized.String())
}

func mergeDiagnostics(target *Diagnostics, source *Diagnostics) {
	if target == nil || source == nil {
		return
	}
	gate := target.SpeechGatePassed
	leadingOffset := target.LeadingSpeechOffsetSeconds
	removed := append([]string(nil), target.RemovedTerminalHallucinations...)
	removed = append(removed, source.RemovedTerminalHallucinations...)
	*target = *source
	target.SpeechGatePassed = gate
	target.LeadingSpeechOffsetSeconds = leadingOffset
	target.RemovedTerminalHallucinations = removed
}

func (r *WhisperServerRunner) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return r.stopProcessLocked(false)
}

func (r *WhisperServerRunner) ProcessID() int {
	return int(r.processID.Load())
}

func (r *WhisperServerRunner) RuntimeEvidence() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(r.processOutputLocked())
}

func (r *WhisperServerRunner) stopProcessLocked(force bool) error {
	if r.cmd == nil {
		return nil
	}
	cmd := r.cmd
	waitDone := r.waitDone
	if cmd.Process != nil {
		if force {
			_ = cmd.Process.Kill()
		} else {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	var err error
	select {
	case err = <-waitDone:
	case <-time.After(2 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		err = <-waitDone
	}
	r.cmd = nil
	r.baseURL = ""
	r.client = nil
	r.waitDone = nil
	r.processID.Store(0)
	if force {
		return nil
	}
	if err != nil && !isExpectedProcessExit(err) {
		return fmt.Errorf("close whisper.cpp server: %w%s", err, r.processDetailLocked())
	}
	return nil
}

func (r *WhisperServerRunner) processDetailLocked() string {
	if detail := trimDetail(r.processOutputLocked()); detail != "" {
		return ": " + detail
	}
	return ""
}

func (r *WhisperServerRunner) processOutputLocked() string {
	var parts []string
	if r.stderr != nil {
		parts = append(parts, r.stderr.String())
	}
	if r.stdout != nil {
		parts = append(parts, r.stdout.String())
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func availableLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve whisper.cpp loopback port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release whisper.cpp loopback port: %w", err)
	}
	return port, nil
}

func normalizedLanguage(value string) string {
	if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
		return value
	}
	return "ru"
}

func closedWaitChannel(err error) chan error {
	ch := make(chan error, 1)
	ch <- err
	return ch
}

func isExpectedProcessExit(err error) bool {
	var exitErr *exec.ExitError
	return err == nil || strings.Contains(fmt.Sprint(err), "signal: terminated") ||
		strings.Contains(fmt.Sprint(err), "signal: killed") ||
		(errors.As(err, &exitErr) && exitErr.ExitCode() == 0)
}
