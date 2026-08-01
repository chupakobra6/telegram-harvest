package transcribe

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWhisperServerRunnerKeepsModelSession(t *testing.T) {
	if os.Getenv("GO_WANT_WHISPER_HELPER") == "1" {
		runWhisperServerHelper()
		return
	}
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-whisper-server")
	script := fmt.Sprintf(`#!/bin/sh
port=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--port" ]; then port="$2"; shift 2; continue; fi
  shift
done
printf 'ggml_metal_init: found device 0\n' >&2
printf 'WHISPER : COREML = 0\n' >&2
GO_WANT_WHISPER_HELPER=1 exec %q -test.run=TestWhisperServerRunnerKeepsModelSession -- "$port"
`, os.Args[0])
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(dir, "fake-ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\ncp \"$6\" \"${13}\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "input.bin")
	if err := os.WriteFile(input, make([]byte, 32044), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &WhisperServerRunner{opts: Options{
		WhisperCommand:   command,
		WhisperModelPath: filepath.Join(dir, "model.bin"),
		WhisperThreads:   4,
		Language:         "ru",
		FFmpegCommand:    ffmpeg,
	}}
	defer runner.Close()

	first, err := runner.RunDetailed(t.Context(), input, filepath.Join(dir, "first.txt"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.RunDetailed(t.Context(), input, filepath.Join(dir, "second.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != "привет мир" || second.Text != first.Text {
		t.Fatalf("texts = %q, %q", first.Text, second.Text)
	}
	if first.ModelColdStartDuration <= 0 {
		t.Fatalf("first cold start = %s", first.ModelColdStartDuration)
	}
	if second.ModelColdStartDuration != 0 {
		t.Fatalf("second cold start = %s, want zero", second.ModelColdStartDuration)
	}
	if first.Backend.Accelerator != AcceleratorMetal || first.Backend.Backend != BackendWhisperCPP {
		t.Fatalf("backend = %+v", first.Backend)
	}
	if first.Diagnostics == nil || first.Diagnostics.Segments != 1 ||
		first.Diagnostics.MeanAverageLogProb != -0.25 ||
		first.Diagnostics.MaximumNoSpeechProb != 0.1 {
		t.Fatalf("diagnostics = %+v", first.Diagnostics)
	}
	if runner.ProcessID() <= 0 {
		t.Fatal("whisper process is not running")
	}
}

func TestWhisperSpeechGateParsesPresenceWithoutTrimmingAudio(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "fake-vad")
	script := `#!/bin/sh
if [ "$FAKE_GATE_RESULT" = "speech" ]; then
  printf 'Detected 2 speech segments:\n'
else
  printf 'Detected 0 speech segments:\n'
fi
`
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		WhisperCommand: "server",
		WhisperSpeechGate: WhisperSpeechGateOptions{
			Enabled:   true,
			Command:   command,
			ModelPath: "silero.bin",
		},
		Environment: map[string]string{"FAKE_GATE_RESULT": "speech"},
	}
	hasSpeech, err := runWhisperSpeechGate(t.Context(), opts, "input.wav")
	if err != nil {
		t.Fatal(err)
	}
	if !hasSpeech {
		t.Fatal("speech gate rejected speech")
	}
	opts.Environment["FAKE_GATE_RESULT"] = "silence"
	hasSpeech, err = runWhisperSpeechGate(t.Context(), opts, "input.wav")
	if err != nil {
		t.Fatal(err)
	}
	if hasSpeech {
		t.Fatal("speech gate accepted no-speech input")
	}
}

func TestWhisperSpeechGateAppliesMinSpeechAfterMinSilence(t *testing.T) {
	minSpeech := 250
	minSilence := 100
	opts := Options{
		WhisperSpeechGate: WhisperSpeechGateOptions{
			Enabled:              true,
			ModelPath:            "silero.bin",
			MinSpeechDurationMS:  &minSpeech,
			MinSilenceDurationMS: &minSilence,
		},
	}
	args := whisperSpeechGateArgs(opts, opts.WhisperSpeechGate.normalized(), "input.wav")
	index := func(flag string) int {
		for current, value := range args {
			if value == flag {
				return current
			}
		}
		return -1
	}
	minSilenceIndex := index("--vad-min-silence-duration-ms")
	minSpeechIndex := index("--vad-min-speech-duration-ms")
	if minSilenceIndex < 0 || minSpeechIndex < 0 || minSilenceIndex >= minSpeechIndex {
		t.Fatalf("gate args do not preserve v1.9.1 min-speech workaround: %v", args)
	}
}

func TestParseLeadingSpeechStart(t *testing.T) {
	start, found, err := parseLeadingSpeechStart("Detected 2 speech segments:\nSpeech segment 0: start = 17949.00, end = 18285.00\n")
	if err != nil {
		t.Fatal(err)
	}
	if !found || start != 179.49 {
		t.Fatalf("start = %.3f, found = %t", start, found)
	}
	start, found, err = parseLeadingSpeechStart("Detected 0 speech segments:\n")
	if err != nil || found || start != 0 {
		t.Fatalf("silence result: start = %.3f, found = %t, err = %v", start, found, err)
	}
}

func TestMergeTranscriptOverlapRemovesOnlyMatchingBoundary(t *testing.T) {
	got := mergeTranscriptOverlap(
		"Первый вопрос. Ответ начинается прямо здесь.",
		"ответ начинается прямо здесь. Затем новый вопрос.",
	)
	if got != "Первый вопрос. Ответ начинается прямо здесь.\n Затем новый вопрос." {
		t.Fatalf("merged transcript = %q", got)
	}
	got = mergeTranscriptOverlap("Одна фраза.", "Совсем другая фраза.")
	if got != "Одна фраза.\n Совсем другая фраза." {
		t.Fatalf("unrelated transcript = %q", got)
	}
}

func TestStripWhisperTerminalHallucinationsIsConservative(t *testing.T) {
	text := "Реальное содержание.\nСубтитры сделал DimaTorzok"
	got, removed := stripWhisperTerminalHallucinations(text)
	if got != "Реальное содержание." {
		t.Fatalf("filtered text = %q", got)
	}
	if len(removed) != 1 || removed[0] != "Субтитры сделал DimaTorzok" {
		t.Fatalf("removed = %#v", removed)
	}

	got, removed = stripWhisperTerminalHallucinations("Он сказал: продолжение следует, но шутил.")
	if got != "Он сказал: продолжение следует, но шутил." || len(removed) != 0 {
		t.Fatalf("embedded phrase was removed: text=%q removed=%#v", got, removed)
	}

	got, removed = stripWhisperTerminalHallucinations("Спасибо.")
	if got != "Спасибо." || len(removed) != 0 {
		t.Fatalf("ordinary thanks was removed: text=%q removed=%#v", got, removed)
	}
}

func runWhisperServerHelper() {
	args := os.Args
	portRaw := args[len(args)-1]
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("/inference", func(w http.ResponseWriter, request *http.Request) {
		if err := request.ParseMultipartForm(2 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(request.FormValue("language")) != "ru" {
			http.Error(w, "language", http.StatusBadRequest)
			return
		}
		expected := map[string]string{
			"response_format": "verbose_json",
			"temperature":     "0",
			"temperature_inc": "0.2",
			"best_of":         "2",
			"beam_size":       "-1",
			"no_speech_thold": "0.6",
			"logprob_thold":   "-1",
			"entropy_thold":   "2.4",
			"suppress_nst":    "false",
		}
		for key, value := range expected {
			if request.FormValue(key) != value {
				http.Error(w, key, http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(whisperResponse{
			Text: " привет мир ",
			Segments: []whisperSegment{{
				AverageLogProbability: -0.25,
				NoSpeechProbability:   0.1,
			}},
		})
	})
	server := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           mux,
		ReadHeaderTimeout: time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		os.Exit(1)
	}
}
