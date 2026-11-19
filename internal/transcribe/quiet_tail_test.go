package transcribe

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRecoverIsolatedTailRequiresAgreedTokenEvidence(t *testing.T) {
	const normalVAD = "Detected 2 speech segments:\nSpeech segment 0: start = 10, end = 80\nSpeech segment 1: start = 450, end = 680"
	for _, test := range []struct {
		name      string
		vad       string
		wantCalls int32
		wantError string
		priorEnd  float64
		clipStart float64
	}{
		{name: "accepts matching complete words", vad: normalVAD, wantCalls: 2},
		{name: "crops past the actual end of a nearby prior phrase", vad: "Detected 2 speech segments:\nSpeech segment 0: start = 50, end = 444\nSpeech segment 1: start = 450, end = 680", wantCalls: 2, priorEnd: 4.32, clipStart: 4.44},
		{name: "silence", vad: "Detected 0 speech segments:", wantError: "no valid uncovered speech island"},
		{name: "overlapping prior speech", vad: "Detected 1 speech segments:\nSpeech segment 0: start = 10, end = 680", wantError: "speech crossing the prior transcript end"},
		{name: "overlapping speech before separate island", vad: "Detected 2 speech segments:\nSpeech segment 0: start = 80, end = 350\nSpeech segment 1: start = 450, end = 680", wantError: "speech crossing the prior transcript end"},
		{name: "multiple uncovered islands", vad: "Detected 2 speech segments:\nSpeech segment 0: start = 340, end = 400\nSpeech segment 1: start = 450, end = 680", wantError: "multiple uncovered speech islands"},
		{name: "disputed completion", vad: normalVAD, wantCalls: 2, wantError: "disagree on the complete text"},
		{name: "divergent spans", vad: normalVAD, wantCalls: 2, wantError: "token spans do not agree"},
		{name: "matching envelope without shared token evidence", vad: normalVAD, wantCalls: 2, wantError: "token spans do not agree"},
		{name: "zero-duration lexical token", vad: normalVAD, wantCalls: 1, wantError: "invalid lexical token timing"},
		{name: "tokens outside detected speech", vad: normalVAD, wantCalls: 1, wantError: "no timed token over the detected speech"},
		{name: "subtitle credit", vad: normalVAD, wantCalls: 1, wantError: "known terminal hallucination"},
		{name: "incomplete lexical tokens", vad: normalVAD, wantCalls: 1, wantError: "tokens do not account for"},
		{name: "segment timestamps overstate coverage", vad: normalVAD, wantCalls: 2, wantError: "before the last detected speech"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			wavPath := filepath.Join(dir, "tail.wav")
			if err := os.WriteFile(wavPath, make([]byte, 44+8*32000), 0o600); err != nil {
				t.Fatal(err)
			}
			vad := filepath.Join(dir, "vad")
			if err := os.WriteFile(vad, []byte("#!/bin/sh\nprintf '%s\\n' '"+test.vad+"'\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			ffmpeg := filepath.Join(dir, "ffmpeg")
			priorEnd, clipStart := 1.0, 4.25
			if test.clipStart > 0 {
				priorEnd, clipStart = test.priorEnd, test.clipStart
			}
			clipDuration := 8 - clipStart
			clipSize := int64(math.Round(clipDuration*32000)) + 44
			const script = `#!/bin/sh
for arg in "$@"; do output="$arg"; done
if [ "$5" = -ss ]; then
  [ "$6" = %.3f ] && [ "$8" = %.3f ] || exit 10
  head -c %d /dev/zero > "$output"
else
  [ "$8" = 'volume=18dB,alimiter=limit=0.90:level=0:latency=1' ] || exit 11
  cp "$6" "$output"
fi
`
			if err := os.WriteFile(ffmpeg, []byte(fmt.Sprintf(script, clipStart, clipDuration, clipSize)), 0o700); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				call := calls.Add(1)
				if err := request.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					return
				}
				defer request.MultipartForm.RemoveAll()
				file, header, err := request.FormFile("file")
				if err != nil {
					t.Error(err)
					return
				}
				_ = file.Close()
				if header.Size != clipSize || strings.Contains(header.Filename, "-gain-") != (call == 2) ||
					request.FormValue("token_timestamps") != "true" || request.FormValue("no_timestamps") != "false" ||
					request.FormValue("language") != "ru" || request.FormValue("prompt") != "" {
					t.Errorf("unexpected decode request %d: size=%d, filename=%s, fields=%v", call, header.Size, header.Filename, request.MultipartForm.Value)
				}
				segmentStart, segmentEnd := 0.0, clipDuration
				start, middle, end := 0.33, 0.50, 2.40
				text, first, second := "Тихая реплика!", "Тихая", " реплика"
				if call == 2 {
					text, first, second = "ТИХАЯ РЕПЛИКА.", "ТИХАЯ", " РЕПЛИКА"
					start, end = 0.35, 2.38
				}
				secondStart := &middle
				switch test.name {
				case "disputed completion":
					if call == 2 {
						text, second = "Тихая догадка", " догадка"
					}
				case "divergent spans":
					if call == 2 {
						start, middle = 0.75, 0.90
					}
				case "matching envelope without shared token evidence":
					start, middle, end = 0.05, 0.30, 2.70
					next := 2.60
					if call == 2 {
						middle, next = 0.20, 2.50
					}
					secondStart = &next
				case "zero-duration lexical token":
					end = middle
				case "tokens outside detected speech":
					start, middle, end = 0.05, 0.10, 0.20
				case "subtitle credit":
					text, first, second = "Редактор субтитров А.Семкин Корректор А.Егорова", "Редактор субтитров", " А.Семкин Корректор А.Егорова"
				case "incomplete lexical tokens":
					second = ""
				case "segment timestamps overstate coverage":
					middle, end = 0.40, 0.50
				}
				_ = json.NewEncoder(w).Encode(whisperResponse{
					Text: text, Duration: clipDuration,
					Segments: []whisperSegment{{Text: text, Start: &segmentStart, End: &segmentEnd,
						Words: []whisperWord{{Word: first, Start: &start, End: &middle}, {Word: second, Start: secondStart, End: &end}, {Word: "!"}},
					}},
				})
			}))
			defer server.Close()
			runner := &WhisperServerRunner{
				opts:    Options{FFmpegCommand: ffmpeg, WhisperSpeechGate: WhisperSpeechGateOptions{Command: vad}},
				baseURL: server.URL, client: server.Client(),
			}
			request := whisperRequestOptions{Language: "ru", InitialPrompt: "unused prompt", CarryInitialPrompt: true}
			segment, err := runner.recoverIsolatedTailLocked(t.Context(), wavPath, priorEnd, 6.8, request, ProductionWhisperAdaptive().normalized())
			if test.wantError == "" {
				if err != nil || segment.Text != "Тихая реплика!" || segment.Start == nil || segment.End == nil ||
					math.Abs(*segment.Start-(clipStart+0.35)) > 0.0001 || math.Abs(*segment.End-(clipStart+2.38)) > 0.0001 {
					t.Fatalf("unexpected recovery: %+v, %v", segment, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("recovery error = %v, want %q", err, test.wantError)
			}
			if count := calls.Load(); count != test.wantCalls {
				t.Fatalf("decode requests = %d, want %d", count, test.wantCalls)
			}
			if paths, err := filepath.Glob(filepath.Join(dir, ".asr-*.wav")); err != nil || len(paths) != 0 {
				t.Fatalf("temporary audio remains: %v, %v", paths, err)
			}
		})
	}
}
