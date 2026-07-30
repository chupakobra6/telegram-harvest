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
		Backend:            BackendWhisperCPP,
		WhisperCommand:     command,
		WhisperModelPath:   filepath.Join(dir, "model.bin"),
		WhisperAccelerator: AcceleratorCPU,
		WhisperThreads:     4,
		Language:           "ru",
		FFmpegCommand:      ffmpeg,
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
	if first.Backend.Accelerator != AcceleratorCPU || first.Backend.Backend != BackendWhisperCPP {
		t.Fatalf("backend = %+v", first.Backend)
	}
	if runner.ProcessID() <= 0 {
		t.Fatal("whisper process is not running")
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(whisperResponse{Text: " привет мир "})
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
