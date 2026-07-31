package mtproto

import (
	"context"
	"errors"
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

	"github.com/chupakobra6/telegram-harvest/internal/harvest"
	"github.com/chupakobra6/telegram-harvest/internal/stages"
	"github.com/chupakobra6/telegram-harvest/internal/transcribe"
)

const (
	asrWorkerAuto               = "auto"
	defaultASRWorkerLimit       = 4
	defaultWorkerRSSBytes       = 768 << 20
	defaultSystemMemoryReserve  = 4 << 30
	autoScaleSafetyMargin       = 1.5
	autoScaleMinimumSaving      = 750 * time.Millisecond
	autoScaleMinimumQueuedAudio = 30.0
	autoScaleCPUUtilizationCeil = 0.80
)

type mediaPipelineJob struct {
	Key              string
	InputPath        string
	TranscriptPath   string
	Record           harvest.MessageRecord
	Attachment       harvest.Attachment
	AttachmentIndex  int
	DownloadSeconds  float64
	EstimatedAudio   float64
	TranscribeOption transcribe.Options
}

type mediaPipelineResult struct {
	Job    mediaPipelineJob
	Result transcribe.Result
	Err    error
	Events []harvest.ASRLogEvent
}

type mediaPipelineResourceSnapshot struct {
	AvailableMemoryBytes uint64
	CPUUtilization       float64
}

type mediaPipelineConfig struct {
	Mode            string
	MaxWorkers      int
	QueueCapacity   int
	Backend         transcribe.Descriptor
	WorkerPolicy    transcribe.WorkerPolicy
	RunnerFactory   func() harvest.Transcriber
	SampleResources func() mediaPipelineResourceSnapshot
	SampleRSS       func(int) uint64
	Now             func() time.Time
}

type mediaPipeline struct {
	ctx    context.Context
	cancel context.CancelFunc
	opts   harvest.HistoryOptions
	cfg    mediaPipelineConfig

	jobs    chan mediaPipelineJob
	wg      sync.WaitGroup
	drained chan struct{}

	mu                sync.Mutex
	closing           bool
	startedAt         time.Time
	completedAt       time.Time
	producerStartedAt time.Time
	producerCompleted time.Time
	workers           []*mediaPipelineWorker
	results           map[string]mediaPipelineResult
	claimed           map[string]struct{}
	queuePeak         int
	jobsSubmitted     int
	jobsDeduplicated  int
	jobsCompleted     int
	jobsFailed        int
	queuedAudio       float64
	activeJobs        int
	peakActiveJobs    int
	totalAudio        float64
	totalASR          float64
	totalStartup      float64
	startupSamples    int
	workerRSS         uint64
	lastResources     mediaPipelineResourceSnapshot
	scaleDecisions    []stages.MediaScaleDecision
	activeWorkers     int
	drainedOnce       sync.Once
	scaleWG           sync.WaitGroup
	scaleMu           sync.Mutex
	resourceDone      chan struct{}
	resourceWG        sync.WaitGroup
	resourceStopOnce  sync.Once
	resourceSamples   int
	resourceCPUTotal  float64
	resourceCPUPeak   float64
}

type mediaPipelineWorker struct {
	id      int
	start   chan struct{}
	active  bool
	runner  harvest.Transcriber
	metrics stages.MediaWorkerMetrics
}

func ParseASRWorkerMode(value string) (mode string, workers int, err error) {
	mode = strings.ToLower(strings.TrimSpace(value))
	if mode == "" {
		mode = asrWorkerAuto
	}
	if mode == asrWorkerAuto {
		return mode, defaultASRWorkerLimit, nil
	}
	workers, err = strconv.Atoi(mode)
	if err != nil || workers < 1 || workers > defaultASRWorkerLimit {
		return "", 0, fmt.Errorf("--asr-workers must be auto or an integer from 1 to %d", defaultASRWorkerLimit)
	}
	return mode, workers, nil
}

func newMediaPipeline(ctx context.Context, opts harvest.HistoryOptions) (*mediaPipeline, error) {
	if !opts.TranscribeMedia {
		return nil, nil
	}
	mode, workers, err := ParseASRWorkerMode(opts.ASRWorkerMode)
	if err != nil {
		return nil, err
	}
	factory := opts.TranscriberFactory
	transcribeOpts := transcribeOptions(opts)
	descriptor := transcribeOpts.Descriptor()
	policy := transcribeOpts.WorkerPolicy()
	if factory == nil && opts.Transcriber != nil {
		runner := opts.Transcriber
		mode = "1"
		workers = 1
		factory = func() harvest.Transcriber { return runner }
	}
	if factory == nil {
		if !transcribeOpts.Configured() {
			return nil, nil
		}
		factory = func() harvest.Transcriber {
			return transcribe.NewManagedRunner(transcribeOpts)
		}
	}
	if mode == asrWorkerAuto {
		workers = policy.AutoMaxWorkers
		if workers < 1 {
			workers = 1
		}
	}
	return newMediaPipelineWithConfig(ctx, opts, mediaPipelineConfig{
		Mode:            mode,
		MaxWorkers:      workers,
		QueueCapacity:   2 * workers,
		Backend:         descriptor,
		WorkerPolicy:    policy,
		RunnerFactory:   factory,
		SampleResources: sampleMediaPipelineResources,
		SampleRSS:       sampleProcessRSS,
		Now:             time.Now,
	})
}

func newMediaPipelineWithConfig(ctx context.Context, opts harvest.HistoryOptions, cfg mediaPipelineConfig) (*mediaPipeline, error) {
	if cfg.MaxWorkers < 1 {
		return nil, fmt.Errorf("media pipeline max workers must be positive")
	}
	if cfg.QueueCapacity < 1 {
		cfg.QueueCapacity = 2 * cfg.MaxWorkers
	}
	if cfg.WorkerPolicy.AutoMaxWorkers == 0 {
		cfg.WorkerPolicy = transcribe.WorkerPolicy{
			Resource:       "cpu",
			AutoMaxWorkers: cfg.MaxWorkers,
			Dynamic:        true,
		}
	}
	if cfg.RunnerFactory == nil {
		return nil, fmt.Errorf("media pipeline runner factory is required")
	}
	if cfg.SampleResources == nil {
		cfg.SampleResources = func() mediaPipelineResourceSnapshot { return mediaPipelineResourceSnapshot{} }
	}
	if cfg.SampleRSS == nil {
		cfg.SampleRSS = func(int) uint64 { return 0 }
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	childCtx, cancel := context.WithCancel(ctx)
	p := &mediaPipeline{
		ctx:               childCtx,
		cancel:            cancel,
		opts:              opts,
		cfg:               cfg,
		jobs:              make(chan mediaPipelineJob, cfg.QueueCapacity),
		drained:           make(chan struct{}),
		results:           make(map[string]mediaPipelineResult),
		claimed:           make(map[string]struct{}),
		producerStartedAt: cfg.Now(),
		resourceDone:      make(chan struct{}),
	}
	p.wg.Add(cfg.MaxWorkers)
	for id := 1; id <= cfg.MaxWorkers; id++ {
		worker := &mediaPipelineWorker{id: id, start: make(chan struct{})}
		worker.metrics.ID = id
		p.workers = append(p.workers, worker)
		go p.runWorker(worker)
	}
	if cfg.Mode == asrWorkerAuto {
		p.activateNextWorkerLocked()
	} else {
		for range cfg.MaxWorkers {
			p.activateNextWorkerLocked()
		}
	}
	p.startResourceMonitor()
	return p, nil
}

func (p *mediaPipeline) claim(key string) bool {
	if p == nil || strings.TrimSpace(key) == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.claimed[key]; ok {
		p.jobsDeduplicated++
		return false
	}
	p.claimed[key] = struct{}{}
	return true
}

func (p *mediaPipeline) releaseClaim(key string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.claimed, key)
	p.mu.Unlock()
}

func (p *mediaPipeline) enqueue(job mediaPipelineJob) error {
	if p == nil {
		return fmt.Errorf("media pipeline is unavailable")
	}
	if job.EstimatedAudio <= 0 {
		job.EstimatedAudio = 30
	}
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		_ = os.Remove(job.InputPath)
		return fmt.Errorf("media pipeline is closed")
	}
	p.jobsSubmitted++
	p.queuedAudio += job.EstimatedAudio
	p.mu.Unlock()

	select {
	case p.jobs <- job:
		p.mu.Lock()
		if queued := len(p.jobs); queued > p.queuePeak {
			p.queuePeak = queued
		}
		p.mu.Unlock()
		if p.cfg.Mode == asrWorkerAuto && p.cfg.WorkerPolicy.Dynamic {
			p.scaleWG.Add(1)
			go func() {
				defer p.scaleWG.Done()
				p.maybeScale()
			}()
		}
		return nil
	case <-p.ctx.Done():
		p.mu.Lock()
		p.queuedAudio -= job.EstimatedAudio
		p.jobsSubmitted--
		delete(p.claimed, job.Key)
		p.mu.Unlock()
		_ = os.Remove(job.InputPath)
		return p.ctx.Err()
	}
}

func (p *mediaPipeline) activateNextWorkerLocked() {
	if p.activeWorkers >= len(p.workers) {
		return
	}
	worker := p.workers[p.activeWorkers]
	worker.active = true
	worker.runner = p.cfg.RunnerFactory()
	p.activeWorkers++
	close(worker.start)
}

func (p *mediaPipeline) runWorker(worker *mediaPipelineWorker) {
	defer p.wg.Done()
	select {
	case <-worker.start:
	case <-p.drained:
		return
	case <-p.ctx.Done():
		return
	}
	defer func() {
		if closer, ok := worker.runner.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				p.mu.Lock()
				p.results[fmt.Sprintf("__worker_close_%d", worker.id)] = mediaPipelineResult{Err: err}
				p.mu.Unlock()
			}
		}
	}()
	for job := range p.jobs {
		p.markJobStarted(job)
		select {
		case <-p.ctx.Done():
			_ = os.Remove(job.InputPath)
			p.storeResult(worker, mediaPipelineResult{Job: job, Err: p.ctx.Err()})
			continue
		default:
		}
		p.mu.Lock()
		if p.startedAt.IsZero() {
			p.startedAt = p.cfg.Now()
		}
		p.mu.Unlock()
		startedAt := p.cfg.Now()
		result := p.processJob(worker, job)
		busy := p.cfg.Now().Sub(startedAt)
		p.storeResult(worker, result)
		p.mu.Lock()
		worker.metrics.BusySeconds += busy.Seconds()
		p.mu.Unlock()
		p.maybeScale()
	}
}

func (p *mediaPipeline) markJobStarted(job mediaPipelineJob) {
	p.mu.Lock()
	p.queuedAudio = math.Max(0, p.queuedAudio-job.EstimatedAudio)
	p.activeJobs++
	if p.activeJobs > p.peakActiveJobs {
		p.peakActiveJobs = p.activeJobs
	}
	p.mu.Unlock()
}

func (p *mediaPipeline) processJob(worker *mediaPipelineWorker, job mediaPipelineJob) mediaPipelineResult {
	defer os.Remove(job.InputPath)
	startEvent := asrLogEvent("transcribe_start", "transcribe", "", job.Record, job.AttachmentIndex, job.Attachment)
	startEvent.DownloadSeconds = job.DownloadSeconds
	startEvent.Engine = job.TranscribeOption.EngineName()
	startEvent.InputBytes = localFileSize(job.InputPath)
	result := mediaPipelineResult{Job: job, Events: []harvest.ASRLogEvent{startEvent}}

	transcribeCtx, cancel := context.WithTimeout(p.ctx, defaultTranscribeTimeout)
	defer cancel()
	transcribeStart := time.Now()
	detailed, err := runTranscriberDetailedAtomic(transcribeCtx, worker.runner, job.TranscribeOption, job.InputPath, job.TranscriptPath)
	if err != nil {
		result.Err = err
		event := asrLogEvent("error", "transcribe", transcriptErrorMessage(err), job.Record, job.AttachmentIndex, job.Attachment)
		event.DownloadSeconds = job.DownloadSeconds
		event.Engine = job.TranscribeOption.EngineName()
		event.InputBytes = localFileSize(job.InputPath)
		event.TotalSeconds = time.Since(transcribeStart).Seconds() + job.DownloadSeconds
		result.Events = append(result.Events, event)
		return result
	}
	result.Result = detailed
	event := asrLogEvent("transcribed", "transcribe", "", job.Record, job.AttachmentIndex, job.Attachment)
	event.DownloadSeconds = job.DownloadSeconds
	event.Engine = detailed.Engine
	event.FFmpegSeconds = detailed.FFmpegDuration.Seconds()
	event.SpeechGateSeconds = detailed.SpeechGateDuration.Seconds()
	event.ModelColdStartSeconds = detailed.ModelColdStartDuration.Seconds()
	event.ASRSeconds = detailed.ASRDuration.Seconds()
	event.TotalSeconds = detailed.TotalDuration.Seconds() + job.DownloadSeconds
	event.InputBytes = detailed.InputBytes
	event.WAVBytes = detailed.WAVBytes
	event.WAVDurationSeconds = detailed.WAVDurationSeconds
	event.TranscriptBytes = detailed.TranscriptBytes
	if diagnostics := detailed.Diagnostics; diagnostics != nil {
		event.ASRSegments = diagnostics.Segments
		event.ASRMeanLogProbability = diagnostics.MeanAverageLogProb
		event.ASRMaxNoSpeechProb = diagnostics.MaximumNoSpeechProb
		event.SpeechGatePassed = diagnostics.SpeechGatePassed
		event.RemovedHallucinations = append([]string(nil), diagnostics.RemovedTerminalHallucinations...)
	}
	if detailed.WAVDurationSeconds > 0 && detailed.ASRDuration > 0 {
		event.RealTimeFactor = detailed.ASRDuration.Seconds() / detailed.WAVDurationSeconds
	}
	result.Events = append(result.Events, event)
	return result
}

func (p *mediaPipeline) storeResult(worker *mediaPipelineWorker, result mediaPipelineResult) {
	audioSeconds := result.Result.WAVDurationSeconds
	if audioSeconds <= 0 {
		audioSeconds = result.Job.Attachment.DurationSeconds
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.results[result.Job.Key] = result
	p.jobsCompleted++
	p.activeJobs = max(0, p.activeJobs-1)
	p.totalAudio += audioSeconds
	p.totalASR += result.Result.ASRDuration.Seconds()
	p.totalStartup += result.Result.ModelColdStartDuration.Seconds()
	if result.Result.ModelColdStartDuration > 0 {
		p.startupSamples++
	}
	if result.Err != nil {
		p.jobsFailed++
		worker.metrics.Failures++
	}
	worker.metrics.Jobs++
	worker.metrics.AudioSeconds += audioSeconds
	worker.metrics.FFmpegSeconds += result.Result.FFmpegDuration.Seconds()
	worker.metrics.SpeechGateSeconds += result.Result.SpeechGateDuration.Seconds()
	worker.metrics.ModelColdStartSeconds += result.Result.ModelColdStartDuration.Seconds()
	worker.metrics.ASRSeconds += result.Result.ASRDuration.Seconds()
	if provider, ok := worker.runner.(interface{ ProcessID() int }); ok {
		if rss := p.cfg.SampleRSS(provider.ProcessID()); rss > worker.metrics.PeakRSSBytes {
			worker.metrics.PeakRSSBytes = rss
		}
		if worker.metrics.PeakRSSBytes > p.workerRSS {
			p.workerRSS = worker.metrics.PeakRSSBytes
		}
	}
	p.completedAt = p.cfg.Now()
	if p.closing && p.jobsCompleted >= p.jobsSubmitted {
		p.drainedOnce.Do(func() { close(p.drained) })
	}
}

func (p *mediaPipeline) maybeScale() {
	p.scaleMu.Lock()
	defer p.scaleMu.Unlock()
	p.mu.Lock()
	idleWorkers := p.activeWorkers - p.activeJobs
	if p.cfg.Mode != asrWorkerAuto || !p.cfg.WorkerPolicy.Dynamic || p.activeWorkers >= p.cfg.MaxWorkers || len(p.jobs) <= idleWorkers {
		p.mu.Unlock()
		return
	}
	workers := p.activeWorkers
	remainingAudio := p.queuedAudio
	speed := speedRatio(p.totalAudio, p.totalASR)
	startup := p.totalStartup / float64(max(1, p.startupSamples))
	workerRSS := p.workerRSS
	p.mu.Unlock()

	if remainingAudio < autoScaleMinimumQueuedAudio {
		p.mu.Lock()
		if len(p.scaleDecisions) < 32 {
			p.scaleDecisions = append(p.scaleDecisions, stages.MediaScaleDecision{
				At:             p.cfg.Now(),
				Workers:        workers,
				RemainingAudio: remainingAudio,
				Action:         "hold",
				Reason:         "queued_audio_below_minimum",
			})
		}
		p.mu.Unlock()
		return
	}
	if speed <= 0 {
		speed = 4
	}
	if startup <= 0 {
		startup = 2
	}
	remainingWork := remainingAudio / speed
	expectedSaving := remainingWork/float64(workers) - remainingWork/float64(workers+1)
	if p.totalAudio == 0 && workers == 1 {
		// The current worker is already busy, so a second worker can consume
		// queued work immediately. Use a conservative bootstrap prior until
		// the first measured result is available.
		expectedSaving = remainingWork
	}
	resources := p.cfg.SampleResources()
	rssEstimate := workerRSS
	if rssEstimate == 0 {
		rssEstimate = defaultWorkerRSSBytes
	}
	action := "hold"
	reason := ""
	if expectedSaving <= startup*autoScaleSafetyMargin+autoScaleMinimumSaving.Seconds() {
		reason = "expected_saving_below_startup_cost"
	} else if resources.CPUUtilization > autoScaleCPUUtilizationCeil {
		reason = "cpu_headroom"
	} else if resources.AvailableMemoryBytes > 0 && resources.AvailableMemoryBytes < rssEstimate+defaultSystemMemoryReserve {
		reason = "memory_headroom"
	} else {
		action = "grow"
	}
	decision := stages.MediaScaleDecision{
		At:              p.cfg.Now(),
		Workers:         workers,
		RemainingAudio:  remainingAudio,
		ExpectedSaving:  expectedSaving,
		AvailableMemory: resources.AvailableMemoryBytes,
		CPUUtilization:  resources.CPUUtilization,
		Action:          action,
		Reason:          reason,
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastResources = resources
	if len(p.scaleDecisions) < 32 {
		p.scaleDecisions = append(p.scaleDecisions, decision)
	}
	if action == "grow" && p.activeWorkers == workers && p.activeWorkers < p.cfg.MaxWorkers && p.queuedAudio > 0 {
		p.activateNextWorkerLocked()
	}
}

func (p *mediaPipeline) waitAndApply(records []harvest.MessageRecord) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		return fmt.Errorf("media pipeline already closed")
	}
	p.closing = true
	p.producerCompleted = p.cfg.Now()
	close(p.jobs)
	if p.jobsCompleted >= p.jobsSubmitted {
		p.drainedOnce.Do(func() { close(p.drained) })
	}
	p.mu.Unlock()
	p.scaleWG.Wait()
	p.wg.Wait()
	p.stopResourceMonitor()
	p.cancel()

	p.mu.Lock()
	results := make(map[string]mediaPipelineResult, len(p.results))
	var closeErrors []error
	for key, result := range p.results {
		if strings.HasPrefix(key, "__worker_close_") {
			closeErrors = append(closeErrors, result.Err)
			continue
		}
		results[key] = result
	}
	p.mu.Unlock()

	ordered := make([]mediaPipelineResult, 0, len(results))
	for _, result := range results {
		ordered = append(ordered, result)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i].Job, ordered[j].Job
		if !left.Record.Date.Equal(right.Record.Date) {
			return left.Record.Date.Before(right.Record.Date)
		}
		if left.Record.Chat.ID != right.Record.Chat.ID {
			return left.Record.Chat.ID < right.Record.Chat.ID
		}
		if left.Record.MessageID != right.Record.MessageID {
			return left.Record.MessageID < right.Record.MessageID
		}
		return left.AttachmentIndex < right.AttachmentIndex
	})
	for _, result := range ordered {
		for _, event := range result.Events {
			emitASRLog(p.opts, event)
		}
	}
	for recordIndex := range records {
		for attachmentIndex := range records[recordIndex].Attachments {
			attachment := &records[recordIndex].Attachments[attachmentIndex]
			result, ok := results[attachment.TranscriptPath]
			if !ok {
				continue
			}
			if result.Err != nil {
				attachment.TranscriptError = transcriptErrorMessage(result.Err)
				continue
			}
			attachment.Transcript = strings.TrimSpace(result.Result.Text)
			if attachment.Transcript == "" {
				if fromFile, err := readTranscriptFile(attachment.TranscriptPath); err == nil {
					attachment.Transcript = fromFile
				}
			}
		}
	}
	metrics := p.metrics()
	stages.ObserveAudioDuration(p.opts.AudioDurationTiming, metrics.AudioSeconds)
	if p.opts.MediaPipelineTiming != nil {
		p.opts.MediaPipelineTiming(metrics)
	}
	return errors.Join(closeErrors...)
}

func (p *mediaPipeline) abort() {
	if p == nil {
		return
	}
	p.cancel()
	p.mu.Lock()
	if !p.closing {
		p.closing = true
		close(p.jobs)
	}
	p.mu.Unlock()
	p.wg.Wait()
	p.stopResourceMonitor()
	for job := range p.jobs {
		_ = os.Remove(job.InputPath)
	}
}

func (p *mediaPipeline) metrics() stages.MediaPipelineMetrics {
	p.mu.Lock()
	defer p.mu.Unlock()
	span := p.completedAt.Sub(p.startedAt)
	if p.startedAt.IsZero() || span < 0 {
		span = 0
	}
	overlapStart := p.startedAt
	if p.producerStartedAt.After(overlapStart) {
		overlapStart = p.producerStartedAt
	}
	overlapEnd := p.completedAt
	if p.producerCompleted.Before(overlapEnd) {
		overlapEnd = p.producerCompleted
	}
	overlap := overlapEnd.Sub(overlapStart)
	if overlap < 0 || p.startedAt.IsZero() {
		overlap = 0
	}
	workers := make([]stages.MediaWorkerMetrics, 0, p.activeWorkers)
	var speechGateSeconds float64
	for _, worker := range p.workers[:p.activeWorkers] {
		metrics := worker.metrics
		if metrics.Jobs == 0 {
			continue
		}
		metrics.ASRSpeedX = speedRatio(metrics.AudioSeconds, metrics.ASRSeconds)
		speechGateSeconds += metrics.SpeechGateSeconds
		workers = append(workers, metrics)
	}
	return stages.MediaPipelineMetrics{
		Backend:                 p.cfg.Backend.Backend,
		Accelerator:             p.cfg.Backend.Accelerator,
		Model:                   p.cfg.Backend.Model,
		WorkerResource:          p.cfg.WorkerPolicy.Resource,
		DynamicWorkers:          p.cfg.WorkerPolicy.Dynamic,
		Mode:                    p.cfg.Mode,
		QueueCapacity:           cap(p.jobs),
		QueuePeak:               p.queuePeak,
		WorkersRequested:        p.cfg.MaxWorkers,
		WorkersActivated:        p.activeWorkers,
		WorkersPeak:             p.peakActiveJobs,
		JobsSubmitted:           p.jobsSubmitted,
		JobsDeduplicated:        p.jobsDeduplicated,
		JobsCompleted:           p.jobsCompleted,
		JobsFailed:              p.jobsFailed,
		AudioSeconds:            p.totalAudio,
		SpeechGateSeconds:       speechGateSeconds,
		SpanSeconds:             span.Seconds(),
		OverlapSeconds:          overlap.Seconds(),
		PoolSpeedX:              speedRatio(p.totalAudio, span.Seconds()),
		WorkerWorkSpeedX:        speedRatio(p.totalAudio, p.totalASR),
		AvailableMemoryBytes:    p.lastResources.AvailableMemoryBytes,
		CPUUtilization:          p.lastResources.CPUUtilization,
		SystemCPUMean:           ratioOrZero(p.resourceCPUTotal, float64(p.resourceSamples)),
		SystemCPUPeak:           p.resourceCPUPeak,
		GPUUtilizationAvailable: false,
		GPUUtilizationReason:    gpuUtilizationUnavailableReason(p.cfg.WorkerPolicy.Resource),
		EstimatedWorkerRSSBytes: p.workerRSS,
		ScaleDecisions:          append([]stages.MediaScaleDecision(nil), p.scaleDecisions...),
		Workers:                 workers,
	}
}

func (p *mediaPipeline) startResourceMonitor() {
	p.resourceWG.Add(1)
	go func() {
		defer p.resourceWG.Done()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-p.resourceDone:
				return
			case <-ticker.C:
				snapshot := p.cfg.SampleResources()
				p.mu.Lock()
				p.lastResources = snapshot
				p.resourceSamples++
				p.resourceCPUTotal += snapshot.CPUUtilization
				if snapshot.CPUUtilization > p.resourceCPUPeak {
					p.resourceCPUPeak = snapshot.CPUUtilization
				}
				p.mu.Unlock()
			}
		}
	}()
}

func (p *mediaPipeline) stopResourceMonitor() {
	if p.resourceDone == nil {
		return
	}
	p.resourceStopOnce.Do(func() { close(p.resourceDone) })
	p.resourceWG.Wait()
}

func runTranscriberDetailedAtomic(
	ctx context.Context,
	runner harvest.Transcriber,
	opts transcribe.Options,
	inputPath string,
	outputPath string,
) (transcribe.Result, error) {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return transcribe.Result{}, fmt.Errorf("prepare transcript dir: %w", err)
	}
	tempFile, err := os.CreateTemp(filepath.Dir(outputPath), ".transcript-*.tmp")
	if err != nil {
		return transcribe.Result{}, fmt.Errorf("create temporary transcript: %w", err)
	}
	tempPath := tempFile.Name()
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return transcribe.Result{}, fmt.Errorf("close temporary transcript: %w", err)
	}
	_ = os.Remove(tempPath)
	defer os.Remove(tempPath)

	result, err := runTranscriberDetailed(ctx, runner, opts, inputPath, tempPath)
	if err != nil {
		return transcribe.Result{}, err
	}
	if _, err := os.Stat(tempPath); os.IsNotExist(err) {
		if err := os.WriteFile(tempPath, []byte(strings.TrimSpace(result.Text)), 0o600); err != nil {
			return transcribe.Result{}, fmt.Errorf("write temporary transcript: %w", err)
		}
	} else if err != nil {
		return transcribe.Result{}, fmt.Errorf("inspect temporary transcript: %w", err)
	}
	file, err := os.OpenFile(tempPath, os.O_RDWR, 0o600)
	if err != nil {
		return transcribe.Result{}, fmt.Errorf("open temporary transcript: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return transcribe.Result{}, fmt.Errorf("sync temporary transcript: %w", err)
	}
	if err := file.Close(); err != nil {
		return transcribe.Result{}, fmt.Errorf("close temporary transcript: %w", err)
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return transcribe.Result{}, fmt.Errorf("secure temporary transcript: %w", err)
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		return transcribe.Result{}, fmt.Errorf("publish transcript: %w", err)
	}
	if result.Text == "" {
		if text, err := readTranscriptFile(outputPath); err == nil {
			result.Text = text
		}
	}
	return result, nil
}

func sampleMediaPipelineResources() mediaPipelineResourceSnapshot {
	return mediaPipelineResourceSnapshot{
		AvailableMemoryBytes: sampleAvailableMemory(),
		CPUUtilization:       sampleCPUUtilization(),
	}
}

func sampleAvailableMemory() uint64 {
	switch runtime.GOOS {
	case "darwin":
		output, err := exec.Command("vm_stat").Output()
		if err != nil {
			return 0
		}
		pageSize := uint64(4096)
		var pages uint64
		for _, line := range strings.Split(string(output), "\n") {
			if strings.Contains(line, "page size of") {
				fields := strings.Fields(line)
				for i, field := range fields {
					if field == "of" && i+1 < len(fields) {
						pageSize, _ = strconv.ParseUint(fields[i+1], 10, 64)
					}
				}
			}
			if !strings.HasPrefix(line, "Pages free:") &&
				!strings.HasPrefix(line, "Pages inactive:") &&
				!strings.HasPrefix(line, "Pages speculative:") &&
				!strings.HasPrefix(line, "Pages purgeable:") {
				continue
			}
			value := strings.TrimSpace(strings.TrimSuffix(strings.SplitN(line, ":", 2)[1], "."))
			count, _ := strconv.ParseUint(value, 10, 64)
			pages += count
		}
		return pages * pageSize
	case "linux":
		content, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0
		}
		for _, line := range strings.Split(string(content), "\n") {
			if strings.HasPrefix(line, "MemAvailable:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kib, _ := strconv.ParseUint(fields[1], 10, 64)
					return kib * 1024
				}
			}
		}
	}
	return 0
}

func sampleCPUUtilization() float64 {
	output, err := exec.Command("ps", "-A", "-o", "%cpu=").Output()
	if err != nil {
		return 0
	}
	total := 0.0
	for _, field := range strings.Fields(string(output)) {
		value, err := strconv.ParseFloat(strings.ReplaceAll(field, ",", "."), 64)
		if err == nil {
			total += value
		}
	}
	utilization := total / (100 * float64(max(1, runtime.NumCPU())))
	return math.Min(1, math.Max(0, utilization))
}

func sampleProcessRSS(pid int) uint64 {
	if pid <= 0 {
		return 0
	}
	output, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	kib, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		return 0
	}
	return kib * 1024
}

func gpuUtilizationUnavailableReason(resource string) string {
	if resource != "gpu" {
		return "not_applicable_for_backend"
	}
	if runtime.GOOS == "darwin" {
		return "macos_gpu_sampler_requires_elevated_powermetrics_access"
	}
	return "gpu_sampler_not_configured"
}

func speedRatio(audioSeconds float64, workSeconds float64) float64 {
	if audioSeconds <= 0 || workSeconds <= 0 {
		return 0
	}
	return audioSeconds / workSeconds
}

func ratioOrZero(numerator, denominator float64) float64 {
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	return numerator / denominator
}
