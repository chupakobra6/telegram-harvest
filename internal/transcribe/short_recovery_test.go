package transcribe

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShortLoopRecoveryUsesValidatedTimestampedPath(t *testing.T) {
	for _, scenario := range []string{"ordinary", "loop", "bad-coverage", "missing-bounds", "late-start", "removed-tail", "timestamped-loop", "middle-loop", "unicode-terminal", "loop-at-start"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			wav := filepath.Join(dir, "input.wav")
			if err := os.WriteFile(wav, make([]byte, 44+34*32000), 0o600); err != nil {
				t.Fatal(err)
			}
			ffmpeg := filepath.Join(dir, "ffmpeg")
			if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nfor arg in \"$@\"; do last=\"$arg\"; done\nhead -c 960044 /dev/zero > \"$last\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls++
				if err := req.ParseMultipartForm(2 << 20); err != nil {
					t.Error(err)
					return
				}
				defer req.MultipartForm.RemoveAll()
				if calls == 1 {
					text := "Обычная запись без повтора."
					if scenario != "ordinary" {
						text = "Название оставим, тогда " + strings.Repeat("давай ", 25) + "конец."
					}
					_ = json.NewEncoder(w).Encode(whisperResponse{Text: text})
					return
				}
				if calls == 2 && req.FormValue("detect_language") == "true" {
					_ = json.NewEncoder(w).Encode(whisperResponse{Language: "russian"})
					return
				}
				if calls == 3 && req.FormValue("no_timestamps") == "false" {
					start, end := 0.1, 33.8
					if scenario == "bad-coverage" {
						end = 1
					}
					text := "Название оставим, комментарий в шапке после номера варианта."
					if scenario == "late-start" {
						start = 30
					}
					if scenario == "timestamped-loop" {
						text = strings.Repeat("давай ", 25)
					}
					segments := []whisperSegment{{Start: &start, End: &end, Text: text}}
					if scenario == "middle-loop" || scenario == "loop-at-start" {
						firstEnd, middleStart, middleEnd, finalStart := 2.0, 2.1, 20.0, 20.1
						first, repeated, terminal := "Название оставим.", strings.Repeat("давай ", 25), "Комментарий в шапке после номера варианта."
						if scenario == "loop-at-start" {
							first = repeated
							repeated = ""
						}
						text = first + "\n" + repeated + "\n" + terminal
						segments = []whisperSegment{{Start: &start, End: &firstEnd, Text: first}, {Start: &middleStart, End: &middleEnd, Text: repeated}, {Start: &finalStart, End: &end, Text: terminal}}
					}
					if scenario == "unicode-terminal" {
						text = "你好，这是最后的内容。"
						segments = []whisperSegment{{Start: &start, End: &end, Text: text}}
					}
					if scenario == "removed-tail" {
						middle, finalStart := 1.0, 31.0
						text = "Только начало.\nСпасибо за просмотр."
						segments = []whisperSegment{{Start: &start, End: &middle, Text: "Только начало."}, {Start: &finalStart, End: &end, Text: "Спасибо за просмотр."}}
					}
					_ = json.NewEncoder(w).Encode(whisperResponse{Text: text, Duration: 34, Segments: segments})
					return
				}
				http.Error(w, fmt.Sprintf("unexpected request %d", calls), http.StatusBadRequest)
			}))
			defer server.Close()
			runner := &WhisperServerRunner{opts: Options{Language: "auto", FFmpegCommand: ffmpeg, WhisperAdaptive: WhisperAdaptiveOptions{Enabled: true, LanguageProbeSeconds: 30, RussianInitialPrompt: longFormRussianInitialPrompt}}, baseURL: server.URL, client: server.Client()}
			lastSpeech := 33.8
			if scenario == "missing-bounds" {
				lastSpeech = 0
			}
			text, diag, recovered, err := runner.inferShortWithLoopRecoveryLocked(t.Context(), wav, 0.1, lastSpeech)
			switch scenario {
			case "ordinary":
				if err != nil || recovered || calls != 1 || text != "Обычная запись без повтора." {
					t.Fatalf("text=%q recovered=%v calls=%d err=%v", text, recovered, calls, err)
				}
			case "loop":
				if err != nil || !recovered || calls != 3 || !strings.Contains(text, "комментарий в шапке") || diag == nil || !diag.CoverageValidated {
					t.Fatalf("text=%q recovered=%v calls=%d diagnostics=%+v err=%v", text, recovered, calls, diag, err)
				}
			case "middle-loop":
				if err != nil || !recovered || !strings.Contains(text, "Комментарий в шапке") {
					t.Fatalf("middle loop: %q %v %v", text, recovered, err)
				}
			case "unicode-terminal":
				if err != nil || !recovered || text != "你好，这是最后的内容。" {
					t.Fatalf("unicode: %q %v %v", text, recovered, err)
				}
			case "bad-coverage", "late-start", "removed-tail", "timestamped-loop", "loop-at-start":
				if err == nil || text != "" || recovered {
					t.Fatalf("unverified text published: %q %v %v", text, recovered, err)
				}
			case "missing-bounds":
				if err == nil || text != "" || recovered || calls != 1 {
					t.Fatalf("missing bounds: %q %v %d %v", text, recovered, calls, err)
				}
			}
		})
	}
}
