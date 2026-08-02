package asrbench

import (
	"testing"

	"github.com/chupakobra6/telegram-harvest/internal/transcribe"
)

func TestValidateVariantAllowsOnlyWhisperMetalExperimentSurface(t *testing.T) {
	valid := Variant{
		Name:        "turbo-beam5",
		Backend:     transcribe.BackendWhisperCPP,
		Accelerator: transcribe.AcceleratorMetal,
		Language:    transcribe.ProductionLanguage,
		Threads:     transcribe.ProductionThreads,
	}
	if err := validateVariant(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Variant){
		"backend":     func(variant *Variant) { variant.Backend = "vosk" },
		"accelerator": func(variant *Variant) { variant.Accelerator = "metal-coreml" },
		"language":    func(variant *Variant) { variant.Language = "en" },
		"threads":     func(variant *Variant) { variant.Threads = 8 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := validateVariant(candidate); err == nil {
				t.Fatalf("variant accepted unsupported %s: %#v", name, candidate)
			}
		})
	}
}

func TestVariantOptionsCanUseCanonicalProductionProfile(t *testing.T) {
	variant := Variant{
		Backend: transcribe.BackendWhisperCPP, Accelerator: transcribe.AcceleratorMetal,
		Command: "/runtime/whisper-server", ModelPath: transcribe.ProductionModelFile,
		Language: transcribe.ProductionLanguage, Threads: transcribe.ProductionThreads,
		FFmpegCommand: "ffmpeg", ProductionProfile: true,
		SpeechGate: transcribe.WhisperSpeechGateOptions{Command: "/runtime/vad", ModelPath: transcribe.ProductionSpeechGateFile},
	}
	opts := variantOptions(variant)
	descriptor := opts.Descriptor()
	if descriptor.Adaptive == nil || descriptor.SpeechGate == nil || opts.WhisperSpeechGate.Command != "/runtime/vad" {
		t.Fatalf("production benchmark options = %#v", descriptor)
	}
}

func TestNormalizeAndQuality(t *testing.T) {
	if got := Normalize(" Ёж, ПРИВЕТ!! 42 "); got != "еж привет 42" {
		t.Fatalf("Normalize() = %q", got)
	}
	samples := []Sample{{ID: "one", Reference: "привет, мир"}}
	perfectWER, perfectCER := Quality(samples, []TranscriptResult{{SampleID: "one", Text: "Привет мир!"}})
	if perfectWER == nil || *perfectWER != 0 || perfectCER == nil || *perfectCER != 0 {
		t.Fatalf("perfect quality = %v, %v", perfectWER, perfectCER)
	}
	wer, cer := Quality(samples, []TranscriptResult{{SampleID: "one", Text: "привет"}})
	if wer == nil || *wer != 0.5 {
		t.Fatalf("WER = %v", wer)
	}
	if cer == nil || *cer <= 0 {
		t.Fatalf("CER = %v", cer)
	}
}

func TestQualityWithoutReferencesIsUnavailable(t *testing.T) {
	wer, cer := Quality([]Sample{{ID: "one"}}, []TranscriptResult{{SampleID: "one", Text: "text"}})
	if wer != nil || cer != nil {
		t.Fatalf("quality = %v, %v", wer, cer)
	}
}

func TestContentQualityExposesCriticalMeaningLoss(t *testing.T) {
	samples := []Sample{{
		ID:        "one",
		Reference: "я сегодня не буду есть тысяча сто калорий",
	}}
	precision, recall, f1, negationRecall, numberRecall := ContentQuality(samples, []TranscriptResult{{
		SampleID: "one",
		Text:     "я сегодня буду есть тысяча калорий",
	}})
	if precision == nil || recall == nil || f1 == nil {
		t.Fatalf("content metrics are unavailable: %v %v %v", precision, recall, f1)
	}
	if *negationRecall != 0 {
		t.Fatalf("negation recall = %f, want 0", *negationRecall)
	}
	if *numberRecall != 0.5 {
		t.Fatalf("number recall = %f, want 0.5", *numberRecall)
	}
	if *recall >= 1 {
		t.Fatalf("word recall = %f, want a detected deletion", *recall)
	}
}
