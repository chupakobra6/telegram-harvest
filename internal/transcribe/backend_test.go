package transcribe

import (
	"path/filepath"
	"testing"
)

func TestBackendDescriptorAndCacheIdentitySeparateVariants(t *testing.T) {
	base := Options{
		Backend:            BackendWhisperCPP,
		WhisperCommand:     "/tmp/whisper-server",
		WhisperModelPath:   "/models/ggml-small.bin",
		WhisperAccelerator: AcceleratorMetal,
		WhisperThreads:     4,
		Language:           "ru",
	}
	coreML := base
	coreML.WhisperAccelerator = AcceleratorMetalCoreML
	quantized := base
	quantized.WhisperModelPath = "/models/ggml-small-q5_0.bin"
	english := base
	english.Language = "en"
	differentBinary := base
	differentBinary.WhisperCommand = "/tmp/whisper-server-next"
	beamSize := 5
	beamSearch := base
	beamSearch.WhisperDecode.BeamSize = &beamSize
	noFallbackIncrement := 0.0
	noFallback := base
	noFallback.WhisperDecode.TemperatureIncrement = &noFallbackIncrement
	speechGate := base
	speechGate.WhisperSpeechGate = WhisperSpeechGateOptions{
		Enabled:   true,
		Command:   "/tmp/whisper-vad-speech-segments",
		ModelPath: "/models/ggml-silero-v6.2.0.bin",
	}

	identities := map[string]bool{}
	for name, opts := range map[string]Options{
		"metal":            base,
		"coreml":           coreML,
		"quantized":        quantized,
		"english":          english,
		"different-binary": differentBinary,
		"beam-search":      beamSearch,
		"no-fallback":      noFallback,
		"speech-gate":      speechGate,
	} {
		identity := opts.CacheIdentity()
		if identity == "" {
			t.Fatalf("%s cache identity is empty", name)
		}
		if identities[identity] {
			t.Fatalf("%s cache identity collides: %s", name, identity)
		}
		identities[identity] = true
	}
	if got := quantized.Descriptor().Quantization; got != "q5_0" {
		t.Fatalf("quantization = %q, want q5_0", got)
	}
	if got := beamSearch.Descriptor().Decode.BeamSize; got != 5 {
		t.Fatalf("beam size = %d, want 5", got)
	}
	if got := noFallback.Descriptor().Decode.TemperatureIncrement; got != 0 {
		t.Fatalf("temperature increment = %f, want 0", got)
	}
	if got := speechGate.Descriptor().SpeechGate.Model; got != "ggml-silero-v6.2.0.bin" {
		t.Fatalf("speech gate model = %q", got)
	}

	commandA := Options{CommandTemplate: "engine-a {input} {output}"}
	commandB := Options{CommandTemplate: "engine-b {input} {output}"}
	if commandA.CacheIdentity() == commandB.CacheIdentity() {
		t.Fatal("external command templates share a transcript cache identity")
	}
}

func TestProductionWhisperProfileIsPinned(t *testing.T) {
	opts := Options{
		Backend:            BackendWhisperCPP,
		WhisperCommand:     "whisper-server",
		WhisperModelPath:   "ggml-large-v3-turbo-q5_0.bin",
		WhisperAccelerator: AcceleratorMetal,
		WhisperDecode:      ProductionWhisperDecode(),
		WhisperSpeechGate:  ProductionWhisperSpeechGate("ggml-silero-v6.2.0.bin"),
	}
	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	descriptor := opts.Descriptor()
	if descriptor.Decode == nil || descriptor.Decode.BeamSize != 5 {
		t.Fatalf("production beam size = %#v, want 5", descriptor.Decode)
	}
	if descriptor.SpeechGate == nil {
		t.Fatal("production speech gate is disabled")
	}
	if descriptor.SpeechGate.Threshold != 0.5 || descriptor.SpeechGate.MinSpeechDurationMS != 250 {
		t.Fatalf("production speech gate = %#v", descriptor.SpeechGate)
	}
	if descriptor.PostFilter != whisperTerminalHallucinationProfile {
		t.Fatalf("production post-filter = %q", descriptor.PostFilter)
	}
}

func TestBackendWorkerPolicyIsResourceSpecific(t *testing.T) {
	vosk := Options{Backend: BackendVosk}.WorkerPolicy()
	if vosk.Resource != "cpu" || !vosk.Dynamic || vosk.AutoMaxWorkers != 4 {
		t.Fatalf("vosk policy = %+v", vosk)
	}
	whisper := Options{Backend: BackendWhisperCPP}.WorkerPolicy()
	if whisper.Resource != "gpu" || whisper.Dynamic || whisper.AutoMaxWorkers != 1 {
		t.Fatalf("whisper policy = %+v", whisper)
	}
}

func TestValidateBackendConfiguration(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		ok   bool
	}{
		{name: "vosk", opts: Options{Backend: BackendVosk, VoskCommand: "vosk", VoskModelPath: "model"}, ok: true},
		{name: "whisper", opts: Options{Backend: BackendWhisperCPP, WhisperCommand: "server", WhisperModelPath: "model", WhisperAccelerator: AcceleratorMetal}, ok: true},
		{name: "missing whisper model", opts: Options{Backend: BackendWhisperCPP, WhisperCommand: "server"}, ok: false},
		{name: "bad accelerator", opts: Options{Backend: BackendWhisperCPP, WhisperCommand: "server", WhisperModelPath: "model", WhisperAccelerator: "magic"}, ok: false},
		{name: "bad no speech threshold", opts: whisperWithNoSpeechThreshold(1.1), ok: false},
		{name: "missing gate model", opts: Options{
			Backend: BackendWhisperCPP, WhisperCommand: "server", WhisperModelPath: "model",
			WhisperSpeechGate: WhisperSpeechGateOptions{Enabled: true},
		}, ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.opts.Validate()
			if (err == nil) != test.ok {
				t.Fatalf("Validate() error = %v, ok = %t", err, test.ok)
			}
		})
	}
}

func whisperWithNoSpeechThreshold(value float64) Options {
	return Options{
		Backend:          BackendWhisperCPP,
		WhisperCommand:   "server",
		WhisperModelPath: "model",
		WhisperDecode: WhisperDecodeOptions{
			NoSpeechThreshold: &value,
		},
	}
}

func TestStableModelIdentityDoesNotEmbedPrivatePath(t *testing.T) {
	opts := Options{
		Backend:          BackendWhisperCPP,
		WhisperCommand:   "server",
		WhisperModelPath: filepath.Join("/Users/private", "models", "ggml-small.bin"),
	}
	if got := opts.Descriptor().Model; got != "ggml-small.bin" {
		t.Fatalf("model identity = %q", got)
	}
}
