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

	identities := map[string]bool{}
	for name, opts := range map[string]Options{
		"metal":            base,
		"coreml":           coreML,
		"quantized":        quantized,
		"english":          english,
		"different-binary": differentBinary,
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

	commandA := Options{CommandTemplate: "engine-a {input} {output}"}
	commandB := Options{CommandTemplate: "engine-b {input} {output}"}
	if commandA.CacheIdentity() == commandB.CacheIdentity() {
		t.Fatal("external command templates share a transcript cache identity")
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
