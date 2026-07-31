package transcribe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	BackendCommand    = "command"
	BackendVosk       = "vosk"
	BackendWhisperCPP = "whispercpp"

	AcceleratorCPU         = "cpu"
	AcceleratorMetal       = "metal"
	AcceleratorMetalCoreML = "metal-coreml"
)

// Descriptor is the stable identity of an ASR implementation. It is used in
// timing reports and transcript cache keys, so fields must describe everything
// that can materially change a transcript.
type Descriptor struct {
	Backend      string                       `json:"backend"`
	Accelerator  string                       `json:"accelerator"`
	Model        string                       `json:"model"`
	Language     string                       `json:"language,omitempty"`
	Quantization string                       `json:"quantization,omitempty"`
	Threads      int                          `json:"threads,omitempty"`
	VADModel     string                       `json:"vad_model,omitempty"`
	Decode       *WhisperDecodeDescriptor     `json:"decode,omitempty"`
	SpeechGate   *WhisperSpeechGateDescriptor `json:"speech_gate,omitempty"`
	PostFilter   string                       `json:"post_filter,omitempty"`
}

// WhisperDecodeOptions contains only material decoder settings. Pointer fields
// distinguish an explicit zero (for example, disabling temperature fallback)
// from the whisper.cpp default.
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

// WorkerPolicy describes safe automatic concurrency for a backend. CPU
// backends may scale when the pipeline proves a benefit; a single Apple GPU
// and unified memory are treated as one shared resource.
type WorkerPolicy struct {
	Resource       string `json:"resource"`
	AutoMaxWorkers int    `json:"auto_max_workers"`
	Dynamic        bool   `json:"dynamic"`
}

func (o Options) Descriptor() Descriptor {
	backend := o.normalizedBackend()
	descriptor := Descriptor{
		Backend:  backend,
		Language: strings.TrimSpace(o.Language),
	}
	switch backend {
	case BackendWhisperCPP:
		descriptor.Accelerator = normalizedWhisperAccelerator(o.WhisperAccelerator)
		descriptor.Model = stableModelIdentity(o.WhisperModelPath)
		descriptor.Quantization = whisperQuantization(o.WhisperModelPath)
		descriptor.Threads = o.WhisperThreads
		descriptor.VADModel = stableModelIdentity(o.WhisperVADModelPath)
		decode := o.WhisperDecode.normalized()
		descriptor.Decode = &decode
		if o.WhisperSpeechGate.Enabled {
			gate := o.WhisperSpeechGate.normalized(o)
			descriptor.SpeechGate = &gate
		}
		descriptor.PostFilter = whisperTerminalHallucinationProfile
	case BackendVosk:
		descriptor.Accelerator = AcceleratorCPU
		descriptor.Model = stableModelIdentity(o.VoskModelPath)
	case BackendCommand:
		descriptor.Accelerator = "external"
		sum := sha256.Sum256([]byte(strings.TrimSpace(o.CommandTemplate)))
		descriptor.Model = "command-" + hex.EncodeToString(sum[:8])
	}
	return descriptor
}

func (o Options) WorkerPolicy() WorkerPolicy {
	switch o.normalizedBackend() {
	case BackendWhisperCPP:
		return WorkerPolicy{Resource: "gpu", AutoMaxWorkers: 1, Dynamic: false}
	case BackendVosk:
		return WorkerPolicy{Resource: "cpu", AutoMaxWorkers: 4, Dynamic: true}
	default:
		return WorkerPolicy{Resource: "external", AutoMaxWorkers: 1, Dynamic: false}
	}
}

func (o Options) CacheIdentity() string {
	descriptor := o.Descriptor()
	descriptorJSON, _ := json.Marshal(descriptor)
	parts := []string{
		"v2",
		string(descriptorJSON),
		cacheModelIdentity(o.engineCommand()),
		cacheModelIdentity(o.modelPath()),
		cacheModelIdentity(o.WhisperVADModelPath),
		cacheModelIdentity(o.WhisperSpeechGate.command(o)),
		cacheModelIdentity(o.WhisperSpeechGate.ModelPath),
	}
	if descriptor.Backend == BackendVosk {
		parts = append(parts, cacheModelIdentity(o.VoskGrammarPath))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func (o Options) engineCommand() string {
	switch o.normalizedBackend() {
	case BackendWhisperCPP:
		return o.WhisperCommand
	case BackendVosk:
		return o.VoskCommand
	default:
		return ""
	}
}

func (o Options) modelPath() string {
	switch o.normalizedBackend() {
	case BackendWhisperCPP:
		return o.WhisperModelPath
	case BackendVosk:
		return o.VoskModelPath
	default:
		return ""
	}
}

func (o Options) Validate() error {
	switch o.normalizedBackend() {
	case BackendCommand:
		if strings.TrimSpace(o.CommandTemplate) == "" {
			return fmt.Errorf("ASR command template is empty")
		}
	case BackendVosk:
		if strings.TrimSpace(o.VoskCommand) == "" {
			return fmt.Errorf("vosk command is empty")
		}
		if strings.TrimSpace(o.VoskModelPath) == "" {
			return fmt.Errorf("vosk model path is empty")
		}
	case BackendWhisperCPP:
		if strings.TrimSpace(o.WhisperCommand) == "" {
			return fmt.Errorf("whisper.cpp server command is empty")
		}
		if strings.TrimSpace(o.WhisperModelPath) == "" {
			return fmt.Errorf("whisper.cpp model path is empty")
		}
		accelerator := normalizedWhisperAccelerator(o.WhisperAccelerator)
		if accelerator != AcceleratorMetal && accelerator != AcceleratorMetalCoreML && accelerator != AcceleratorCPU {
			return fmt.Errorf("unsupported whisper.cpp accelerator %q", accelerator)
		}
		if err := o.WhisperDecode.validate(); err != nil {
			return err
		}
		if err := o.WhisperSpeechGate.validate(o); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported ASR backend %q", o.Backend)
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

// ProductionWhisperDecode is the single decode profile used by daily
// catch-ups. Beam search recovered content and numbers that greedy decoding
// omitted on the private outgoing-Russian benchmark while remaining faster
// than the Telegram stage.
func ProductionWhisperDecode() WhisperDecodeOptions {
	beamSize := 5
	return WhisperDecodeOptions{BeamSize: &beamSize}
}

// ProductionWhisperSpeechGate is a whole-file speech-presence check. It does
// not cut audio, so detected speech always reaches Whisper unchanged.
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

func (o WhisperSpeechGateOptions) normalized(_ Options) WhisperSpeechGateDescriptor {
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
	value := o.normalized(parent)
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

func (o Options) normalizedBackend() string {
	if strings.TrimSpace(o.CommandTemplate) != "" {
		return BackendCommand
	}
	if backend := strings.ToLower(strings.TrimSpace(o.Backend)); backend != "" {
		return backend
	}
	if strings.TrimSpace(o.WhisperCommand) != "" || strings.TrimSpace(o.WhisperModelPath) != "" {
		return BackendWhisperCPP
	}
	return BackendVosk
}

func normalizedWhisperAccelerator(value string) string {
	if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
		return value
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		return AcceleratorMetal
	}
	return AcceleratorCPU
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
