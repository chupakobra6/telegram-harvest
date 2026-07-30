package transcribe

import (
	"crypto/sha256"
	"encoding/hex"
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
	Backend      string `json:"backend"`
	Accelerator  string `json:"accelerator"`
	Model        string `json:"model"`
	Language     string `json:"language,omitempty"`
	Quantization string `json:"quantization,omitempty"`
	Threads      int    `json:"threads,omitempty"`
	VADModel     string `json:"vad_model,omitempty"`
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
	parts := []string{
		"v1",
		descriptor.Backend,
		descriptor.Accelerator,
		descriptor.Model,
		cacheModelIdentity(o.engineCommand()),
		cacheModelIdentity(o.modelPath()),
		descriptor.Language,
		descriptor.Quantization,
		strconv.Itoa(descriptor.Threads),
		cacheModelIdentity(o.WhisperVADModelPath),
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
	default:
		return fmt.Errorf("unsupported ASR backend %q", o.Backend)
	}
	return nil
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
