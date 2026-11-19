package transcribe

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	quietTailLeadSeconds                 = 0.25
	quietTailSpanToleranceSeconds        = 0.25
	quietTailGainDB                      = 18.0
	quietTailLimiterPeak                 = 0.90
	quietTailSubtitleCredit              = "редактор субтитров а семкин корректор а егорова"
	quietTailWAVDurationToleranceSeconds = 0.002 // FFmpeg may add a small WAV metadata chunk.
)

// Recover only an isolated, previously uncovered speech island. Token timing
// from both decodes limits the evidence to audio they actually transcribed.
func (r *WhisperServerRunner) recoverIsolatedTailLocked(ctx context.Context, tailPath string, coveredEnd, lastSpeech float64, request whisperRequestOptions, settings WhisperAdaptiveDescriptor) (whisperSegment, error) {
	tailDuration := wavPCM16MonoDuration(fileSize(tailPath))
	if !(coveredEnd >= 0 && coveredEnd < tailDuration && lastSpeech > coveredEnd && lastSpeech <= tailDuration+1 && tailDuration <= settings.TrailingRecoveryMaxSeconds+quietTailWAVDurationToleranceSeconds) {
		return whisperSegment{}, fmt.Errorf("invalid isolated tail bounds")
	}
	islands, err := runSpeechProbe(ctx, r.opts, tailPath)
	if err != nil {
		return whisperSegment{}, err
	}
	var island vadSpeechSegment
	isolationFloor := coveredEnd
	for _, candidate := range islands {
		if candidate.end <= coveredEnd {
			continue
		}
		if candidate.start < coveredEnd {
			// VAD and Whisper can put the end of the same phrase a few
			// frames apart. A larger crossing is speech we cannot skip.
			if candidate.end-coveredEnd > quietTailSpanToleranceSeconds {
				return whisperSegment{}, fmt.Errorf("isolated tail contains speech crossing the prior transcript end")
			}
			isolationFloor = candidate.end
			continue
		}
		if island.end > 0 {
			return whisperSegment{}, fmt.Errorf("isolated tail has multiple uncovered speech islands")
		}
		island = candidate
	}
	if !(island.end > island.start && island.end <= tailDuration) {
		return whisperSegment{}, fmt.Errorf("isolated tail has no valid uncovered speech island")
	}
	// The FFmpeg range helper addresses milliseconds. Round forward so the
	// isolated clip cannot reintroduce audio before the accepted transcript.
	offset := math.Ceil(math.Max(isolationFloor, island.start-quietTailLeadSeconds)*1000) / 1000
	isolatedPath, cleanup, err := temporaryWAV(filepath.Dir(tailPath), ".asr-isolated-tail-*.wav")
	if err != nil {
		return whisperSegment{}, err
	}
	defer cleanup()
	if err := extractASRWAVRange(ctx, r.opts, tailPath, isolatedPath, offset, tailDuration-offset); err != nil {
		return whisperSegment{}, err
	}
	request = whisperRequestOptions{Language: request.Language, SegmentTimestamps: true, TokenTimestamps: true}
	raw, err := r.inferRequestLocked(ctx, isolatedPath, request)
	if err != nil {
		return whisperSegment{}, err
	}
	isolatedSize := fileSize(isolatedPath)
	isolatedDuration := wavPCM16MonoDuration(isolatedSize)
	isolatedLastSpeech := lastSpeech - offset
	isolatedIsland := vadSpeechSegment{start: island.start - offset, end: island.end - offset}
	rawSpan, err := isolatedTailTokenSpan(raw, isolatedDuration, isolatedLastSpeech, settings.TrailingCoverageToleranceSeconds, isolatedIsland)
	if err != nil {
		return whisperSegment{}, err
	}

	boostedPath, cleanupBoosted, err := temporaryWAV(filepath.Dir(tailPath), ".asr-isolated-tail-gain-*.wav")
	if err != nil {
		return whisperSegment{}, err
	}
	defer cleanupBoosted()
	if err := boostIsolatedTail(ctx, r.opts, isolatedPath, boostedPath); err != nil {
		return whisperSegment{}, err
	}
	if fileSize(boostedPath) != isolatedSize {
		return whisperSegment{}, fmt.Errorf("isolated tail gain changed the WAV length")
	}
	boosted, err := r.inferRequestLocked(ctx, boostedPath, request)
	if err != nil {
		return whisperSegment{}, err
	}
	boostedSpan, err := isolatedTailTokenSpan(boosted, isolatedDuration, isolatedLastSpeech, settings.TrailingCoverageToleranceSeconds, isolatedIsland)
	if err != nil {
		return whisperSegment{}, err
	}
	if rawSpan.lexicalText != boostedSpan.lexicalText {
		return whisperSegment{}, fmt.Errorf("isolated tail decodes disagree on the complete text")
	}
	start := offset + math.Max(rawSpan.start, boostedSpan.start)
	end := offset + math.Min(rawSpan.end, boostedSpan.end)
	if math.Abs(rawSpan.start-boostedSpan.start) > quietTailSpanToleranceSeconds ||
		math.Abs(rawSpan.end-boostedSpan.end) > quietTailSpanToleranceSeconds ||
		!overlappingTailTokens(rawSpan.speechTokens, boostedSpan.speechTokens) {
		return whisperSegment{}, fmt.Errorf("isolated tail token spans do not agree over the detected speech")
	}
	rawDiagnostics := whisperDiagnostics(raw.Segments)
	boostedDiagnostics := whisperDiagnostics(boosted.Segments)
	segment := whisperSegment{
		Text:                  strings.TrimSpace(raw.Text),
		Start:                 &start,
		End:                   &end,
		AverageLogProbability: math.Min(rawDiagnostics.MeanAverageLogProb, boostedDiagnostics.MeanAverageLogProb),
		NoSpeechProbability:   math.Max(rawDiagnostics.MaximumNoSpeechProb, boostedDiagnostics.MaximumNoSpeechProb),
	}
	checked := whisperResponse{Text: segment.Text, Duration: tailDuration, Segments: []whisperSegment{segment}}
	if _, err := validateTimestampedLongFormResponse(checked, tailDuration, lastSpeech, settings.TrailingCoverageToleranceSeconds); err != nil {
		return whisperSegment{}, err
	}
	return segment, ctx.Err()
}

func boostIsolatedTail(ctx context.Context, opts Options, input, output string) error {
	ffmpeg := strings.TrimSpace(opts.FFmpegCommand)
	if ffmpeg == "" {
		ffmpeg = DefaultFFmpegCommand
	}
	filter := fmt.Sprintf("volume=%gdB,alimiter=limit=%.2f:level=0:latency=1", quietTailGainDB, quietTailLimiterPeak)
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", input, "-af", filter, "-ac", "1", "-ar", "16000", "-sample_fmt", "s16", output}
	if detail, err := exec.CommandContext(ctx, ffmpeg, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("boost isolated ASR tail: %w: %s", err, trimDetail(string(detail)))
	}
	return nil
}

type isolatedTailSpan struct {
	lexicalText  string
	start, end   float64
	speechTokens []vadSpeechSegment
}

func overlappingTailTokens(raw, boosted []vadSpeechSegment) bool {
	for i, j := 0, 0; i < len(raw) && j < len(boosted); {
		if math.Max(raw[i].start, boosted[j].start) < math.Min(raw[i].end, boosted[j].end) {
			return true
		}
		if raw[i].end <= boosted[j].end {
			i++
		} else {
			j++
		}
	}
	return false
}

func isolatedTailTokenSpan(response whisperResponse, duration, lastSpeech, tolerance float64, island vadSpeechSegment) (isolatedTailSpan, error) {
	if !(response.Duration > 0 && math.Abs(response.Duration-duration) <= quietTailWAVDurationToleranceSeconds) {
		return isolatedTailSpan{}, fmt.Errorf("isolated tail response duration does not match its WAV")
	}
	if _, err := validateTimestampedLongFormResponse(response, duration, lastSpeech, tolerance); err != nil {
		return isolatedTailSpan{}, err
	}
	text := normalizeTailLexicalText(response.Text)
	if text == "" {
		return isolatedTailSpan{}, fmt.Errorf("isolated tail returned no lexical speech")
	}
	normalizedHallucination := " " + normalizeWhisperHallucination(response.Text)
	if strings.HasSuffix(normalizedHallucination, " "+quietTailSubtitleCredit) {
		return isolatedTailSpan{}, fmt.Errorf("isolated tail returned a known terminal hallucination")
	}
	for known := range whisperTerminalHallucinations {
		if strings.HasSuffix(normalizedHallucination, " "+known) {
			return isolatedTailSpan{}, fmt.Errorf("isolated tail returned a known terminal hallucination")
		}
	}
	var segments strings.Builder
	start, previousStart, end := -1.0, -1.0, -1.0
	limit := math.Min(response.Duration, duration)
	var speechTokens []vadSpeechSegment
	for _, segment := range response.Segments {
		var words strings.Builder
		for _, word := range segment.Words {
			words.WriteString(word.Word)
			if normalizeTailLexicalText(word.Word) == "" {
				continue
			}
			if word.Start == nil || word.End == nil ||
				!(*word.Start >= 0 && *word.Start >= previousStart && *word.End > *word.Start && *word.End >= end && *word.End <= limit) {
				return isolatedTailSpan{}, fmt.Errorf("isolated tail returned invalid lexical token timing")
			}
			if start < 0 {
				start = *word.Start
			}
			speechStart := math.Max(*word.Start, island.start)
			speechEnd := math.Min(*word.End, island.end)
			if speechStart < speechEnd {
				speechTokens = append(speechTokens, vadSpeechSegment{start: speechStart, end: speechEnd})
			}
			previousStart, end = *word.Start, *word.End
		}
		if normalizeTailLexicalText(words.String()) != normalizeTailLexicalText(segment.Text) {
			return isolatedTailSpan{}, fmt.Errorf("isolated tail tokens do not account for the segment text")
		}
		segments.WriteString(segment.Text)
		segments.WriteByte(' ')
	}
	if start < 0 || normalizeTailLexicalText(segments.String()) != text {
		return isolatedTailSpan{}, fmt.Errorf("isolated tail tokens do not account for the full text")
	}
	if len(speechTokens) == 0 {
		return isolatedTailSpan{}, fmt.Errorf("isolated tail has no timed token over the detected speech")
	}
	return isolatedTailSpan{lexicalText: text, start: start, end: end, speechTokens: speechTokens}, nil
}

func normalizeTailLexicalText(text string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(text), func(current rune) bool {
		return !(unicode.IsLetter(current) || unicode.IsNumber(current) || unicode.IsMark(current))
	}), " ")
}
