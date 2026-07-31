package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	Segments []whisperSegment `json:"segments,omitempty"`
}

type whisperSegment struct {
	AverageLogProbability float64 `json:"avg_logprob"`
	NoSpeechProbability   float64 `json:"no_speech_prob"`
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

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return Result{}, fmt.Errorf("whisper.cpp session is closed")
	}
	diagnostics := &Diagnostics{}
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
	text, inferenceDiagnostics, err := r.inferLocked(ctx, wavPath)
	inferenceDuration := time.Since(inferenceStart)
	if r.opts.StageTiming != nil {
		r.opts.StageTiming(stages.ASR, inferenceDuration)
	}
	if err != nil {
		return Result{}, err
	}
	mergeDiagnostics(diagnostics, inferenceDiagnostics)
	asrDuration := speechGateDuration + inferenceDuration
	text = strings.TrimSpace(text)
	text, diagnostics.RemovedTerminalHallucinations = stripWhisperTerminalHallucinations(text)
	if err := os.WriteFile(outputPath, []byte(text), 0o600); err != nil {
		return Result{}, fmt.Errorf("write transcript: %w", err)
	}
	return Result{
		Text:                   text,
		Engine:                 r.opts.EngineName(),
		Backend:                r.opts.Descriptor(),
		FFmpegDuration:         ffmpegDuration,
		ModelColdStartDuration: modelColdStartDuration,
		ASRDuration:            asrDuration,
		SpeechGateDuration:     speechGateDuration,
		TotalDuration:          time.Since(start),
		InputBytes:             fileSize(inputPath),
		WAVBytes:               wavBytes,
		WAVDurationSeconds:     wavPCM16MonoDuration(wavBytes),
		TranscriptBytes:        int64(len([]byte(text))),
		Diagnostics:            diagnostics,
	}, nil
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
	file, err := os.Open(wavPath)
	if err != nil {
		return "", nil, fmt.Errorf("open whisper.cpp WAV: %w", err)
	}
	bodyReader, bodyWriter := io.Pipe()
	writer := multipart.NewWriter(bodyWriter)
	contentType := writer.FormDataContentType()
	go streamWhisperRequest(writer, bodyWriter, file, wavPath, normalizedLanguage(r.opts.Language), r.opts.WhisperDecode)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/inference", bodyReader)
	if err != nil {
		_ = bodyReader.Close()
		return "", nil, fmt.Errorf("prepare whisper.cpp request: %w", err)
	}
	request.Header.Set("Content-Type", contentType)
	response, err := r.client.Do(request)
	if err != nil {
		return "", nil, fmt.Errorf("whisper.cpp inference: %w%s", err, r.processDetailLocked())
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return "", nil, fmt.Errorf("read whisper.cpp response: %w", err)
	}
	var decoded whisperResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", nil, fmt.Errorf("decode whisper.cpp response: %w: %s", err, trimDetail(string(payload)))
	}
	if response.StatusCode != http.StatusOK {
		detail := strings.TrimSpace(decoded.Error)
		if detail == "" {
			detail = trimDetail(string(payload))
		}
		return "", nil, fmt.Errorf("whisper.cpp inference status %s: %s", response.Status, detail)
	}
	if detail := strings.TrimSpace(decoded.Error); detail != "" {
		return "", nil, fmt.Errorf("whisper.cpp inference: %s", detail)
	}
	return decoded.Text, whisperDiagnostics(decoded.Segments), nil
}

func streamWhisperRequest(
	writer *multipart.Writer,
	pipe *io.PipeWriter,
	file *os.File,
	wavPath string,
	language string,
	decode WhisperDecodeOptions,
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
	for key, value := range map[string]string{
		"response_format": "verbose_json",
		"language":        language,
		"temperature":     strconv.FormatFloat(settings.Temperature, 'f', -1, 64),
		"temperature_inc": strconv.FormatFloat(settings.TemperatureIncrement, 'f', -1, 64),
		"best_of":         strconv.Itoa(settings.BestOf),
		"beam_size":       strconv.Itoa(settings.BeamSize),
		"no_speech_thold": strconv.FormatFloat(settings.NoSpeechThreshold, 'f', -1, 64),
		"logprob_thold":   strconv.FormatFloat(settings.LogProbabilityThreshold, 'f', -1, 64),
		"entropy_thold":   strconv.FormatFloat(settings.EntropyThreshold, 'f', -1, 64),
		"suppress_nst":    strconv.FormatBool(settings.SuppressNonSpeechTokens),
	} {
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
	return []string{
		"--no-prints",
		"--vad-model", strings.TrimSpace(opts.WhisperSpeechGate.ModelPath),
		"--vad-threshold", strconv.FormatFloat(gate.Threshold, 'f', -1, 64),
		// whisper.cpp v1.9.1 accidentally assigns the min-silence value to
		// min-speech. Sending min-speech second preserves both the intended
		// gate behavior on v1.9.1 and the correct behavior after upstream fixes.
		"--vad-min-silence-duration-ms", strconv.Itoa(gate.MinSilenceDurationMS),
		"--vad-min-speech-duration-ms", strconv.Itoa(gate.MinSpeechDurationMS),
		"--vad-speech-pad-ms", strconv.Itoa(gate.SpeechPadMS),
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
	removed := target.RemovedTerminalHallucinations
	*target = *source
	target.SpeechGatePassed = gate
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
