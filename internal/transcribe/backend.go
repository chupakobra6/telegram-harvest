package transcribe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/chupakobra6/telegram-harvest/internal/stages"
)

const (
	BackendWhisperCPP        = "whispercpp"
	AcceleratorMetal         = "metal"
	ProductionLanguage       = "ru"
	ProductionThreads        = 4
	ProductionModelFile      = "ggml-large-v3-turbo-q5_0.bin"
	ProductionSpeechGateFile = "ggml-silero-v6.2.0.bin"
)

// Descriptor is the stable identity of the production ASR implementation.
// It is used in timing reports and transcript cache keys, so fields must
// describe everything that can materially change a transcript.
type Descriptor struct {
	Backend      string                       `json:"backend"`
	Accelerator  string                       `json:"accelerator"`
	Model        string                       `json:"model"`
	Language     string                       `json:"language,omitempty"`
	Quantization string                       `json:"quantization,omitempty"`
	Threads      int                          `json:"threads,omitempty"`
	Decode       *WhisperDecodeDescriptor     `json:"decode,omitempty"`
	SpeechGate   *WhisperSpeechGateDescriptor `json:"speech_gate,omitempty"`
	PostFilter   string                       `json:"post_filter,omitempty"`
}

// WhisperDecodeOptions is intentionally retained as a benchmark-level
// contract. Daily and dump always use ProductionWhisperDecode.
type WhisperDecodeOptions struct {
	BeamSize                *int     `json:"beam_size,omitempty"`
	BestOf                  *int     `json:"best_of,omitempty"`
	Temperature             *float64 `json:"temperature,omitempty"`
	TemperatureIncrement    *float64 `json:"temperature_increment,omitempty"`
	NoSpeechThreshold       *float64 `json:"no_speech_threshold,omitempty"`
	LogProbabilityThreshold *float64 `json:"log_probability_threshold,omitempty"`
	EntropyThreshold        *float64 `json:"entropy_threshold,omitempty"`
	SuppressNonSpeechTokens bool     `json:"suppress_non_speech_tokens,omitempty"`
}

type WhisperDecodeDescriptor struct {
	BeamSize                int     `json:"beam_size"`
	BestOf                  int     `json:"best_of"`
	Temperature             float64 `json:"temperature"`
	TemperatureIncrement    float64 `json:"temperature_increment"`
	NoSpeechThreshold       float64 `json:"no_speech_threshold"`
	LogProbabilityThreshold float64 `json:"log_probability_threshold"`
	EntropyThreshold        float64 `json:"entropy_threshold"`
	SuppressNonSpeechTokens bool    `json:"suppress_non_speech_tokens"`
}

// WhisperSpeechGateOptions runs Silero only as a whole-file speech-presence
// check. Unlike integrated VAD, it never cuts or concatenates detected speech.
type WhisperSpeechGateOptions struct {
	Enabled              bool     `json:"enabled,omitempty"`
	Command              string   `json:"command,omitempty"`
	ModelPath            string   `json:"model_path,omitempty"`
	Threshold            *float64 `json:"threshold,omitempty"`
	MinSpeechDurationMS  *int     `json:"min_speech_duration_ms,omitempty"`
	MinSilenceDurationMS *int     `json:"min_silence_duration_ms,omitempty"`
	SpeechPadMS          *int     `json:"speech_pad_ms,omitempty"`
}

type WhisperSpeechGateDescriptor struct {
	Enabled              bool    `json:"enabled"`
	Model                string  `json:"model"`
	Threshold            float64 `json:"threshold"`
	MinSpeechDurationMS  int     `json:"min_speech_duration_ms"`
	MinSilenceDurationMS int     `json:"min_silence_duration_ms"`
	SpeechPadMS          int     `json:"speech_pad_ms"`
}

// Diagnostics are backend confidence signals. They are evidence for benchmark
// analysis, not a generic automatic rejection rule.
type Diagnostics struct {
	Segments                      int      `json:"segments"`
	MeanAverageLogProb            float64  `json:"mean_average_log_probability,omitempty"`
	MinimumAverageLogProb         float64  `json:"minimum_average_log_probability,omitempty"`
	MeanNoSpeechProb              float64  `json:"mean_no_speech_probability,omitempty"`
	MaximumNoSpeechProb           float64  `json:"maximum_no_speech_probability,omitempty"`
	SpeechGatePassed              *bool    `json:"speech_gate_passed,omitempty"`
	RemovedTerminalHallucinations []string `json:"removed_terminal_hallucinations,omitempty"`
}

// ProductionOptions is the only ASR profile used by product commands. Paths
// remain configurable because the runtime is a local dependency; inference
// behavior is deliberately code-owned and regression-tested.
func ProductionOptions(command, modelPath, gateModelPath, ffmpegCommand string, timing stages.Observer) Options {
	return Options{
		WhisperCommand:    command,
		WhisperModelPath:  modelPath,
		WhisperThreads:    ProductionThreads,
		WhisperDecode:     ProductionWhisperDecode(),
		WhisperSpeechGate: ProductionWhisperSpeechGate(gateModelPath),
		Language:          ProductionLanguage,
		FFmpegCommand:     ffmpegCommand,
		StageTiming:       timing,
		productionProfile: true,
	}
}

func (o Options) Descriptor() Descriptor {
	decode := o.WhisperDecode.normalized()
	descriptor := Descriptor{
		Backend:      BackendWhisperCPP,
		Accelerator:  AcceleratorMetal,
		Model:        stableModelIdentity(o.WhisperModelPath),
		Language:     normalizedLanguage(o.Language),
		Quantization: whisperQuantization(o.WhisperModelPath),
		Threads:      normalizedThreads(o.WhisperThreads),
		Decode:       &decode,
		PostFilter:   whisperTerminalHallucinationProfile,
	}
	if o.WhisperSpeechGate.Enabled {
		gate := o.WhisperSpeechGate.normalized()
		descriptor.SpeechGate = &gate
	}
	return descriptor
}

// CacheIdentity keeps the v2 layout used by the selected production profile.
// The empty fifth component is the removed integrated-VAD slot; preserving it
// lets existing tuned-Whisper transcripts remain reusable.
func (o Options) CacheIdentity() string {
	descriptorJSON, _ := json.Marshal(o.Descriptor())
	parts := []string{
		"v2",
		string(descriptorJSON),
		cacheModelIdentity(o.WhisperCommand),
		cacheModelIdentity(o.WhisperModelPath),
		"",
		cacheModelIdentity(o.WhisperSpeechGate.command(o)),
		cacheModelIdentity(o.WhisperSpeechGate.ModelPath),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func (o Options) Validate() error {
	if strings.TrimSpace(o.WhisperCommand) == "" {
		return fmt.Errorf("whisper.cpp server command is empty")
	}
	if strings.TrimSpace(o.WhisperModelPath) == "" {
		return fmt.Errorf("whisper.cpp model path is empty")
	}
	if o.productionProfile && filepath.Base(filepath.Clean(o.WhisperModelPath)) != ProductionModelFile {
		return fmt.Errorf("production whisper.cpp model must be %s", ProductionModelFile)
	}
	if err := o.WhisperDecode.validate(); err != nil {
		return err
	}
	if err := o.WhisperSpeechGate.validate(o); err != nil {
		return err
	}
	if o.productionProfile && filepath.Base(filepath.Clean(o.WhisperSpeechGate.ModelPath)) != ProductionSpeechGateFile {
		return fmt.Errorf("production whisper speech-gate model must be %s", ProductionSpeechGateFile)
	}
	return nil
}

func (o WhisperDecodeOptions) normalized() WhisperDecodeDescriptor {
	return WhisperDecodeDescriptor{
		BeamSize:                intValue(o.BeamSize, -1),
		BestOf:                  intValue(o.BestOf, 2),
		Temperature:             floatValue(o.Temperature, 0),
		TemperatureIncrement:    floatValue(o.TemperatureIncrement, 0.2),
		NoSpeechThreshold:       floatValue(o.NoSpeechThreshold, 0.6),
		LogProbabilityThreshold: floatValue(o.LogProbabilityThreshold, -1),
		EntropyThreshold:        floatValue(o.EntropyThreshold, 2.4),
		SuppressNonSpeechTokens: o.SuppressNonSpeechTokens,
	}
}

func ProductionWhisperDecode() WhisperDecodeOptions {
	beamSize := 5
	return WhisperDecodeOptions{BeamSize: &beamSize}
}

func ProductionWhisperSpeechGate(modelPath string) WhisperSpeechGateOptions {
	threshold := 0.5
	minSpeechDurationMS := 250
	minSilenceDurationMS := 100
	speechPadMS := 30
	return WhisperSpeechGateOptions{
		Enabled:              true,
		ModelPath:            modelPath,
		Threshold:            &threshold,
		MinSpeechDurationMS:  &minSpeechDurationMS,
		MinSilenceDurationMS: &minSilenceDurationMS,
		SpeechPadMS:          &speechPadMS,
	}
}

func (o WhisperDecodeOptions) validate() error {
	value := o.normalized()
	if value.BeamSize == 0 || value.BeamSize < -1 {
		return fmt.Errorf("whisper beam size must be -1 or positive")
	}
	if value.BestOf < 1 {
		return fmt.Errorf("whisper best-of must be positive")
	}
	if value.Temperature < 0 || value.TemperatureIncrement < 0 {
		return fmt.Errorf("whisper temperatures must be non-negative")
	}
	if value.NoSpeechThreshold < 0 || value.NoSpeechThreshold > 1 {
		return fmt.Errorf("whisper no-speech threshold must be between 0 and 1")
	}
	return nil
}

func (o WhisperSpeechGateOptions) normalized() WhisperSpeechGateDescriptor {
	return WhisperSpeechGateDescriptor{
		Enabled:              o.Enabled,
		Model:                stableModelIdentity(o.ModelPath),
		Threshold:            floatValue(o.Threshold, 0.5),
		MinSpeechDurationMS:  intValue(o.MinSpeechDurationMS, 250),
		MinSilenceDurationMS: intValue(o.MinSilenceDurationMS, 100),
		SpeechPadMS:          intValue(o.SpeechPadMS, 30),
	}
}

func (o WhisperSpeechGateOptions) validate(parent Options) error {
	if !o.Enabled {
		return nil
	}
	if strings.TrimSpace(o.command(parent)) == "" {
		return fmt.Errorf("whisper speech-gate command is empty")
	}
	if strings.TrimSpace(o.ModelPath) == "" {
		return fmt.Errorf("whisper speech-gate model is empty")
	}
	value := o.normalized()
	if value.Threshold < 0 || value.Threshold > 1 {
		return fmt.Errorf("whisper speech-gate threshold must be between 0 and 1")
	}
	if value.MinSpeechDurationMS < 0 || value.MinSilenceDurationMS < 0 || value.SpeechPadMS < 0 {
		return fmt.Errorf("whisper speech-gate durations must be non-negative")
	}
	return nil
}

func (o WhisperSpeechGateOptions) command(parent Options) string {
	if value := strings.TrimSpace(o.Command); value != "" {
		return value
	}
	server := strings.TrimSpace(parent.WhisperCommand)
	if server == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(server), "whisper-vad-speech-segments")
}

func normalizedThreads(threads int) int {
	if threads > 0 {
		return threads
	}
	return ProductionThreads
}

func intValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func floatValue(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

func stableModelIdentity(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Base(filepath.Clean(path))
}

func cacheModelIdentity(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	cleaned, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		cleaned = filepath.Clean(path)
	}
	info, statErr := os.Stat(cleaned)
	if statErr != nil {
		return cleaned
	}
	return strings.Join([]string{
		cleaned,
		strconv.FormatInt(info.Size(), 10),
		strconv.FormatInt(info.ModTime().UnixNano(), 10),
	}, ":")
}

func whisperQuantization(path string) string {
	name := strings.ToLower(filepath.Base(path))
	for _, marker := range []string{"q2_k", "q3_k", "q4_0", "q4_1", "q4_k", "q5_0", "q5_1", "q5_k", "q6_k", "q8_0"} {
		if strings.Contains(name, marker) {
			return marker
		}
	}
	return "f16"
}
