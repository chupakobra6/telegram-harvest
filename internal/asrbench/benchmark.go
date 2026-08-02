package asrbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/chupakobra6/telegram-harvest/internal/transcribe"
)

type Manifest struct {
	Name                string    `json:"name"`
	ReferenceProvenance string    `json:"reference_provenance,omitempty"`
	Samples             []Sample  `json:"samples"`
	Variants            []Variant `json:"variants"`
}

type Sample struct {
	ID        string `json:"id"`
	InputPath string `json:"input_path"`
	Reference string `json:"reference,omitempty"`
	Speech    bool   `json:"speech"`
}

type Variant struct {
	Name              string                              `json:"name"`
	Backend           string                              `json:"backend"`
	Command           string                              `json:"command"`
	ModelPath         string                              `json:"model_path"`
	Accelerator       string                              `json:"accelerator"`
	Language          string                              `json:"language,omitempty"`
	Threads           int                                 `json:"threads,omitempty"`
	FFmpegCommand     string                              `json:"ffmpeg_command,omitempty"`
	ExpectedEvidence  string                              `json:"expected_evidence,omitempty"`
	Environment       map[string]string                   `json:"environment,omitempty"`
	Decode            transcribe.WhisperDecodeOptions     `json:"decode,omitempty"`
	SpeechGate        transcribe.WhisperSpeechGateOptions `json:"speech_gate,omitempty"`
	ProductionProfile bool                                `json:"production_profile,omitempty"`
}

type Report struct {
	Name                string          `json:"name"`
	CorpusHash          string          `json:"corpus_hash"`
	CreatedAt           time.Time       `json:"created_at"`
	AudioSeconds        float64         `json:"audio_seconds"`
	Samples             int             `json:"samples"`
	Referenced          int             `json:"referenced_samples"`
	ReferenceProvenance string          `json:"reference_provenance,omitempty"`
	Runs                int             `json:"runs"`
	GPUUtilization      Availability    `json:"gpu_utilization"`
	Variants            []VariantResult `json:"variants"`
}

type Availability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type VariantResult struct {
	Name                    string                `json:"name"`
	Backend                 transcribe.Descriptor `json:"backend"`
	Runs                    []RunResult           `json:"runs"`
	MedianASRSpeedX         float64               `json:"median_asr_speed_x"`
	MedianPipelineSpeedX    float64               `json:"median_pipeline_speed_x"`
	MedianColdStart         float64               `json:"median_cold_start_seconds"`
	PeakRSSBytes            uint64                `json:"peak_rss_bytes"`
	MeanProcessCPU          float64               `json:"mean_process_cpu_percent"`
	PeakProcessCPU          float64               `json:"peak_process_cpu_percent"`
	WER                     *float64              `json:"wer,omitempty"`
	CER                     *float64              `json:"cer,omitempty"`
	WordPrecision           *float64              `json:"word_precision,omitempty"`
	WordRecall              *float64              `json:"word_recall,omitempty"`
	WordF1                  *float64              `json:"word_f1,omitempty"`
	NegationRecall          *float64              `json:"negation_recall,omitempty"`
	NumberRecall            *float64              `json:"number_recall,omitempty"`
	EmptyTranscripts        int                   `json:"empty_transcripts"`
	FailedTranscripts       int                   `json:"failed_transcripts"`
	MissedSpeech            int                   `json:"missed_speech"`
	NoSpeechHallucinations  int                   `json:"no_speech_hallucinations"`
	ClearlyWrongTranscripts int                   `json:"clearly_wrong_transcripts"`
	EvidenceMatched         bool                  `json:"evidence_matched"`
	Evidence                string                `json:"evidence,omitempty"`
	Transcripts             []TranscriptResult    `json:"transcripts"`
}

type RunResult struct {
	WallSeconds       float64 `json:"wall_seconds"`
	AudioSeconds      float64 `json:"audio_seconds"`
	FFmpegSeconds     float64 `json:"ffmpeg_seconds"`
	ASRSeconds        float64 `json:"asr_seconds"`
	SpeechGateSeconds float64 `json:"speech_gate_seconds"`
	ColdStartSeconds  float64 `json:"cold_start_seconds"`
	ASRSpeedX         float64 `json:"asr_speed_x"`
	PipelineSpeedX    float64 `json:"pipeline_speed_x"`
	PeakRSSBytes      uint64  `json:"peak_rss_bytes"`
	MeanProcessCPU    float64 `json:"mean_process_cpu_percent"`
	PeakProcessCPU    float64 `json:"peak_process_cpu_percent"`
}

type TranscriptResult struct {
	SampleID    string                  `json:"sample_id"`
	Text        string                  `json:"text,omitempty"`
	Error       string                  `json:"error,omitempty"`
	Diagnostics *transcribe.Diagnostics `json:"diagnostics,omitempty"`
}

type processProvider interface {
	ProcessID() int
}

type evidenceProvider interface {
	RuntimeEvidence() string
}

func LoadManifest(path string) (Manifest, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return Manifest{}, fmt.Errorf("manifest name is required")
	}
	if len(manifest.Samples) == 0 || len(manifest.Variants) == 0 {
		return Manifest{}, fmt.Errorf("manifest requires samples and variants")
	}
	base := filepath.Dir(path)
	for index := range manifest.Samples {
		sample := &manifest.Samples[index]
		if !filepath.IsAbs(sample.InputPath) {
			sample.InputPath = filepath.Join(base, sample.InputPath)
		}
		if sample.ID == "" {
			sample.ID = filepath.Base(sample.InputPath)
		}
		if _, err := os.Stat(sample.InputPath); err != nil {
			return Manifest{}, fmt.Errorf("sample %s: %w", sample.ID, err)
		}
	}
	for _, variant := range manifest.Variants {
		if err := validateVariant(variant); err != nil {
			return Manifest{}, fmt.Errorf("variant %q: %w", variant.Name, err)
		}
	}
	return manifest, nil
}

func validateVariant(variant Variant) error {
	if strings.TrimSpace(variant.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if variant.Backend != transcribe.BackendWhisperCPP {
		return fmt.Errorf("backend must be %q", transcribe.BackendWhisperCPP)
	}
	if variant.Accelerator != transcribe.AcceleratorMetal {
		return fmt.Errorf("accelerator must be %q", transcribe.AcceleratorMetal)
	}
	if variant.Language != transcribe.ProductionLanguage {
		return fmt.Errorf("language must be %q", transcribe.ProductionLanguage)
	}
	if variant.Threads != transcribe.ProductionThreads {
		return fmt.Errorf("threads must be %d", transcribe.ProductionThreads)
	}
	return nil
}

func Run(ctx context.Context, manifest Manifest, runs int, workDir string) (Report, error) {
	if runs < 1 {
		return Report{}, fmt.Errorf("runs must be positive")
	}
	hash, err := CorpusHash(manifest.Samples)
	if err != nil {
		return Report{}, err
	}
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		return Report{}, fmt.Errorf("prepare benchmark work dir: %w", err)
	}
	report := Report{
		Name:                manifest.Name,
		CorpusHash:          hash,
		CreatedAt:           time.Now().UTC(),
		Samples:             len(manifest.Samples),
		Referenced:          referencedCount(manifest.Samples),
		ReferenceProvenance: manifest.ReferenceProvenance,
		Runs:                runs,
		GPUUtilization:      GPUUtilizationAvailability(),
	}
	for _, variant := range manifest.Variants {
		result, variantErr := runVariant(ctx, variant, manifest.Samples, runs, workDir)
		if variantErr != nil {
			return Report{}, fmt.Errorf("variant %s: %w", variant.Name, variantErr)
		}
		report.Variants = append(report.Variants, result)
		if report.AudioSeconds == 0 && len(result.Runs) > 0 {
			report.AudioSeconds = result.Runs[0].AudioSeconds
		}
	}
	return report, nil
}

func runVariant(ctx context.Context, variant Variant, samples []Sample, runs int, workDir string) (VariantResult, error) {
	opts := variantOptions(variant)
	if err := opts.Validate(); err != nil {
		return VariantResult{}, err
	}
	result := VariantResult{
		Name:    variant.Name,
		Backend: opts.Descriptor(),
	}
	var asrSpeeds, pipelineSpeeds, coldStarts []float64
	var cpuWeighted float64
	var cpuSamples int
	for runIndex := range runs {
		runner := transcribe.NewManagedRunner(opts)
		runDir := filepath.Join(workDir, safeName(variant.Name), fmt.Sprintf("run-%02d", runIndex+1))
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			_ = runner.Close()
			return VariantResult{}, err
		}
		monitor := startMonitor(runner)
		startedAt := time.Now()
		var runResult RunResult
		var transcripts []TranscriptResult
		for sampleIndex, sample := range samples {
			output := filepath.Join(runDir, fmt.Sprintf("%03d.txt", sampleIndex+1))
			detailed, err := runner.RunDetailed(ctx, sample.InputPath, output)
			transcript := TranscriptResult{
				SampleID:    sample.ID,
				Text:        strings.TrimSpace(detailed.Text),
				Diagnostics: detailed.Diagnostics,
			}
			if err != nil {
				transcript.Error = err.Error()
			}
			transcripts = append(transcripts, transcript)
			runResult.AudioSeconds += detailed.WAVDurationSeconds
			runResult.FFmpegSeconds += detailed.FFmpegDuration.Seconds()
			runResult.ASRSeconds += detailed.ASRDuration.Seconds()
			runResult.SpeechGateSeconds += detailed.SpeechGateDuration.Seconds()
			runResult.ColdStartSeconds += detailed.ModelColdStartDuration.Seconds()
		}
		runResult.WallSeconds = time.Since(startedAt).Seconds()
		evidence := ""
		if provider, ok := runner.(evidenceProvider); ok {
			evidence = provider.RuntimeEvidence()
		}
		closeErr := runner.Close()
		resources := monitor.stop()
		if closeErr != nil {
			return VariantResult{}, closeErr
		}
		runResult.PeakRSSBytes = resources.peakRSS
		runResult.MeanProcessCPU = resources.meanCPU()
		runResult.PeakProcessCPU = resources.peakCPU
		runResult.ASRSpeedX = ratio(runResult.AudioSeconds, runResult.ASRSeconds)
		runResult.PipelineSpeedX = ratio(runResult.AudioSeconds, runResult.WallSeconds)
		result.Runs = append(result.Runs, runResult)
		asrSpeeds = append(asrSpeeds, runResult.ASRSpeedX)
		pipelineSpeeds = append(pipelineSpeeds, runResult.PipelineSpeedX)
		coldStarts = append(coldStarts, runResult.ColdStartSeconds)
		if runResult.PeakRSSBytes > result.PeakRSSBytes {
			result.PeakRSSBytes = runResult.PeakRSSBytes
		}
		if runResult.PeakProcessCPU > result.PeakProcessCPU {
			result.PeakProcessCPU = runResult.PeakProcessCPU
		}
		if resources.samples > 0 {
			cpuWeighted += resources.cpuTotal
			cpuSamples += resources.samples
		}
		if runIndex == 0 {
			result.Transcripts = transcripts
			result.Evidence = trimEvidence(evidence)
			result.EvidenceMatched = variant.ExpectedEvidence == "" ||
				strings.Contains(strings.ToLower(evidence), strings.ToLower(variant.ExpectedEvidence))
		}
	}
	result.MedianASRSpeedX = median(asrSpeeds)
	result.MedianPipelineSpeedX = median(pipelineSpeeds)
	result.MedianColdStart = median(coldStarts)
	if cpuSamples > 0 {
		result.MeanProcessCPU = cpuWeighted / float64(cpuSamples)
	}
	samplesByID := make(map[string]Sample, len(samples))
	for _, sample := range samples {
		samplesByID[sample.ID] = sample
	}
	for _, transcript := range result.Transcripts {
		sample := samplesByID[transcript.SampleID]
		empty := strings.TrimSpace(transcript.Text) == ""
		wrong := false
		if transcript.Error != "" {
			result.FailedTranscripts++
			wrong = true
		}
		if transcript.Error == "" && empty {
			result.EmptyTranscripts++
		}
		if sample.Speech && empty {
			result.MissedSpeech++
			wrong = true
		}
		if !sample.Speech && !empty {
			result.NoSpeechHallucinations++
			wrong = true
		}
		if wrong {
			result.ClearlyWrongTranscripts++
		}
	}
	result.WER, result.CER = Quality(samples, result.Transcripts)
	result.WordPrecision, result.WordRecall, result.WordF1,
		result.NegationRecall, result.NumberRecall = ContentQuality(samples, result.Transcripts)
	return result, nil
}

func variantOptions(variant Variant) transcribe.Options {
	if variant.ProductionProfile {
		opts := transcribe.ProductionOptions(
			variant.Command,
			variant.ModelPath,
			variant.SpeechGate.ModelPath,
			variant.FFmpegCommand,
			nil,
		)
		opts.Environment = variant.Environment
		opts.WhisperSpeechGate.Command = variant.SpeechGate.Command
		return opts
	}
	return transcribe.Options{
		WhisperCommand:    variant.Command,
		WhisperModelPath:  variant.ModelPath,
		WhisperThreads:    variant.Threads,
		Language:          variant.Language,
		FFmpegCommand:     variant.FFmpegCommand,
		Environment:       variant.Environment,
		WhisperDecode:     variant.Decode,
		WhisperSpeechGate: variant.SpeechGate,
	}
}

func CorpusHash(samples []Sample) (string, error) {
	hash := sha256.New()
	for _, sample := range samples {
		content, err := os.ReadFile(sample.InputPath)
		if err != nil {
			return "", fmt.Errorf("hash sample %s: %w", sample.ID, err)
		}
		_, _ = hash.Write([]byte(sample.ID))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(Normalize(sample.Reference)))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func Quality(samples []Sample, transcripts []TranscriptResult) (*float64, *float64) {
	byID := make(map[string]TranscriptResult, len(transcripts))
	for _, transcript := range transcripts {
		byID[transcript.SampleID] = transcript
	}
	var referenceWords, hypothesisWords []string
	var referenceRunes, hypothesisRunes []rune
	for _, sample := range samples {
		if strings.TrimSpace(sample.Reference) == "" {
			continue
		}
		reference := Normalize(sample.Reference)
		hypothesis := Normalize(byID[sample.ID].Text)
		referenceWords = append(referenceWords, strings.Fields(reference)...)
		hypothesisWords = append(hypothesisWords, strings.Fields(hypothesis)...)
		referenceRunes = append(referenceRunes, []rune(strings.ReplaceAll(reference, " ", ""))...)
		hypothesisRunes = append(hypothesisRunes, []rune(strings.ReplaceAll(hypothesis, " ", ""))...)
	}
	if len(referenceWords) == 0 || len(referenceRunes) == 0 {
		return nil, nil
	}
	wer := float64(editDistance(referenceWords, hypothesisWords)) / float64(len(referenceWords))
	cer := float64(editDistance(referenceRunes, hypothesisRunes)) / float64(len(referenceRunes))
	return &wer, &cer
}

// ContentQuality complements order-sensitive WER with multiset word overlap.
// Exact recalls for negations and number concepts expose small but potentially
// meaning-changing deletions that aggregate WER can hide.
func ContentQuality(samples []Sample, transcripts []TranscriptResult) (
	precision *float64,
	recall *float64,
	f1 *float64,
	negationRecall *float64,
	numberRecall *float64,
) {
	byID := make(map[string]TranscriptResult, len(transcripts))
	for _, transcript := range transcripts {
		byID[transcript.SampleID] = transcript
	}
	referenceCounts := map[string]int{}
	hypothesisCounts := map[string]int{}
	referenceNegations := map[string]int{}
	hypothesisNegations := map[string]int{}
	referenceNumbers := map[string]int{}
	hypothesisNumbers := map[string]int{}
	for _, sample := range samples {
		if strings.TrimSpace(sample.Reference) == "" {
			continue
		}
		countQualityTokens(strings.Fields(Normalize(sample.Reference)), referenceCounts, referenceNegations, referenceNumbers)
		countQualityTokens(strings.Fields(Normalize(byID[sample.ID].Text)), hypothesisCounts, hypothesisNegations, hypothesisNumbers)
	}
	if sumCounts(referenceCounts) == 0 {
		return nil, nil, nil, nil, nil
	}
	matches := overlapCounts(referenceCounts, hypothesisCounts)
	precisionValue := ratio(float64(matches), float64(sumCounts(hypothesisCounts)))
	recallValue := ratio(float64(matches), float64(sumCounts(referenceCounts)))
	f1Value := 0.0
	if precisionValue+recallValue > 0 {
		f1Value = 2 * precisionValue * recallValue / (precisionValue + recallValue)
	}
	precision = &precisionValue
	recall = &recallValue
	f1 = &f1Value
	if total := sumCounts(referenceNegations); total > 0 {
		value := float64(overlapCounts(referenceNegations, hypothesisNegations)) / float64(total)
		negationRecall = &value
	}
	if total := sumCounts(referenceNumbers); total > 0 {
		value := float64(overlapCounts(referenceNumbers, hypothesisNumbers)) / float64(total)
		numberRecall = &value
	}
	return precision, recall, f1, negationRecall, numberRecall
}

func countQualityTokens(tokens []string, all, negations, numbers map[string]int) {
	for _, token := range tokens {
		all[token]++
		if isNegation(token) {
			negations[token]++
		}
		if isNumberConcept(token) {
			numbers[token]++
		}
	}
}

func overlapCounts(left, right map[string]int) int {
	total := 0
	for token, count := range left {
		total += min(count, right[token])
	}
	return total
}

func sumCounts(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func isNegation(token string) bool {
	switch token {
	case "не", "нет", "ни", "без", "нельзя", "никогда", "ничего", "никто":
		return true
	default:
		return false
	}
}

func isNumberConcept(token string) bool {
	if strings.IndexFunc(token, func(current rune) bool { return !unicode.IsDigit(current) }) == -1 {
		return token != ""
	}
	switch token {
	case "ноль", "один", "одна", "одно", "два", "две", "три", "четыре", "пять",
		"шесть", "семь", "восемь", "девять", "десять", "одиннадцать", "двенадцать",
		"тринадцать", "четырнадцать", "пятнадцать", "шестнадцать", "семнадцать",
		"восемнадцать", "девятнадцать", "двадцать", "тридцать", "сорок", "пятьдесят",
		"шестьдесят", "семьдесят", "восемьдесят", "девяносто", "сто", "двести",
		"триста", "четыреста", "пятьсот", "шестьсот", "семьсот", "восемьсот",
		"девятьсот", "тысяча", "тысячи", "тысяч", "миллион", "миллиона", "миллионов":
		return true
	default:
		return false
	}
}

func Normalize(value string) string {
	value = strings.ReplaceAll(strings.ToLower(value), "ё", "е")
	var builder strings.Builder
	space := true
	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsNumber(current) {
			builder.WriteRune(current)
			space = false
		} else if !space {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func editDistance[T comparable](left, right []T) int {
	previous := make([]int, len(right)+1)
	for index := range previous {
		previous[index] = index
	}
	for leftIndex, leftValue := range left {
		current := make([]int, len(right)+1)
		current[0] = leftIndex + 1
		for rightIndex, rightValue := range right {
			cost := 1
			if leftValue == rightValue {
				cost = 0
			}
			current[rightIndex+1] = min(
				previous[rightIndex+1]+1,
				current[rightIndex]+1,
				previous[rightIndex]+cost,
			)
		}
		previous = current
	}
	return previous[len(right)]
}

type monitor struct {
	done chan struct{}
	wg   sync.WaitGroup
	mu   sync.Mutex
	resourceSamples
}

type resourceSamples struct {
	peakRSS  uint64
	cpuTotal float64
	peakCPU  float64
	samples  int
}

func (r resourceSamples) meanCPU() float64 {
	if r.samples == 0 {
		return 0
	}
	return r.cpuTotal / float64(r.samples)
}

func startMonitor(runner transcribe.ManagedRunner) *monitor {
	m := &monitor{done: make(chan struct{})}
	provider, ok := runner.(processProvider)
	if !ok {
		return m
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-m.done:
				return
			case <-ticker.C:
				pid := provider.ProcessID()
				if pid <= 0 {
					continue
				}
				rss, cpu := sampleProcess(pid)
				m.mu.Lock()
				m.samples++
				m.cpuTotal += cpu
				if cpu > m.peakCPU {
					m.peakCPU = cpu
				}
				if rss > m.peakRSS {
					m.peakRSS = rss
				}
				m.mu.Unlock()
			}
		}
	}()
	return m
}

func (m *monitor) stop() resourceSamples {
	close(m.done)
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resourceSamples
}

func sampleProcess(pid int) (uint64, float64) {
	output, err := exec.Command("ps", "-o", "rss=,%cpu=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, 0
	}
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return 0, 0
	}
	rssKiB, _ := strconv.ParseUint(fields[0], 10, 64)
	cpu, _ := strconv.ParseFloat(strings.ReplaceAll(fields[1], ",", "."), 64)
	return rssKiB * 1024, cpu
}

func GPUUtilizationAvailability() Availability {
	if runtime.GOOS == "darwin" {
		return Availability{
			Reason: "powermetrics GPU sampling requires elevated privileges; runtime Metal evidence is recorded instead",
		}
	}
	return Availability{Reason: "GPU sampler is not configured on this platform"}
}

func referencedCount(samples []Sample) int {
	count := 0
	for _, sample := range samples {
		if strings.TrimSpace(sample.Reference) != "" {
			count++
		}
	}
	return count
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	middle := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[middle-1] + sorted[middle]) / 2
	}
	return sorted[middle]
}

func ratio(numerator, denominator float64) float64 {
	if numerator <= 0 || denominator <= 0 || math.IsNaN(denominator) {
		return 0
	}
	return numerator / denominator
}

func safeName(value string) string {
	var builder strings.Builder
	for _, current := range strings.ToLower(value) {
		if unicode.IsLetter(current) || unicode.IsNumber(current) || current == '-' || current == '_' {
			builder.WriteRune(current)
		} else {
			builder.WriteByte('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

func trimEvidence(value string) string {
	const limit = 12_000
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}
