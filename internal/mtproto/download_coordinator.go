package mtproto

import (
	"context"
	"sync"
	"time"

	"github.com/chupakobra6/telegram-harvest/internal/stages"
)

const (
	downloadCoordinatorPolicyName = "page-fifo-cap2"
	downloadCoordinatorSlots      = 2
)

type mediaDownloadTask struct {
	Slots    int
	Transfer func(context.Context)
	Fail     func(error)
	After    func()
	Cancel   func(error)
}

type downloadCoordinator struct {
	gate        sync.RWMutex
	batchGate   chan struct{}
	mu          sync.Mutex
	metrics     stages.DownloadCoordinatorMetrics
	activeFiles int
	observer    stages.DownloadTransferObserver
	reserve     func(context.Context) error
	finished    bool
}

func newDownloadCoordinator(observer stages.DownloadTransferObserver, reserve func(context.Context) error) *downloadCoordinator {
	return &downloadCoordinator{
		batchGate: make(chan struct{}, 1),
		metrics: stages.DownloadCoordinatorMetrics{
			Policy:        downloadCoordinatorPolicyName,
			CapacitySlots: downloadCoordinatorSlots,
		},
		observer: observer,
		reserve:  reserve,
	}
}

func (c *downloadCoordinator) runHistory(fn func() error) error {
	if c == nil {
		return fn()
	}
	c.gate.Lock()
	defer c.gate.Unlock()
	c.mu.Lock()
	c.metrics.HistorySections++
	if c.metrics.ActiveSlots != 0 {
		c.metrics.HistoryDownloadOverlap++
	}
	c.mu.Unlock()
	return fn()
}

func (c *downloadCoordinator) runBatch(ctx context.Context, tasks []*mediaDownloadTask, stageObserver stages.Observer) error {
	if len(tasks) == 0 {
		return nil
	}
	if c != nil {
		select {
		case c.batchGate <- struct{}{}:
			defer func() { <-c.batchGate }()
		case <-ctx.Done():
			cancelDownloadTasks(tasks, ctx.Err())
			return ctx.Err()
		}
	}
	queuedAt := time.Now()
	if c != nil {
		c.mu.Lock()
		c.metrics.Batches++
		c.metrics.Jobs += len(tasks)
		for _, task := range tasks {
			if downloadTaskSlots(task) == 2 {
				c.metrics.LargeJobs++
			} else {
				c.metrics.SmallJobs++
			}
		}
		c.mu.Unlock()
	}

	for index := 0; index < len(tasks); {
		if err := ctx.Err(); err != nil {
			cancelDownloadTasks(tasks[index:], err)
			return err
		}
		first := tasks[index]
		if downloadTaskSlots(first) == 1 && index+1 < len(tasks) && downloadTaskSlots(tasks[index+1]) == 1 {
			if c != nil {
				c.mu.Lock()
				c.metrics.SmallParallelPairs++
				c.mu.Unlock()
			}
			if err := c.runWave(ctx, []*mediaDownloadTask{first, tasks[index+1]}, queuedAt, stageObserver); err != nil {
				cancelDownloadTasks(tasks[index+2:], err)
				return err
			}
			index += 2
			continue
		}
		if err := c.runWave(ctx, []*mediaDownloadTask{first}, queuedAt, stageObserver); err != nil {
			cancelDownloadTasks(tasks[index+1:], err)
			return err
		}
		index++
	}
	return ctx.Err()
}

func (c *downloadCoordinator) runWave(ctx context.Context, tasks []*mediaDownloadTask, queuedAt time.Time, stageObserver stages.Observer) error {
	if err := ctx.Err(); err != nil {
		cancelDownloadTasks(tasks, err)
		return err
	}
	waveStarted := time.Now()
	if c != nil {
		c.gate.RLock()
		c.noteWaveStarted(tasks, queuedAt)
	}
	// One reservation covers one two-slot wave, matching the existing large-file
	// behavior: a single paced transfer may use two gotd chunk workers. A pair of
	// one-worker files never creates more chunk concurrency than that path.
	reserveErr := c.reserveWave(ctx)
	if reserveErr == nil {
		var workers sync.WaitGroup
		workers.Add(len(tasks))
		for _, task := range tasks {
			go func(task *mediaDownloadTask) {
				defer workers.Done()
				if task != nil && task.Transfer != nil {
					task.Transfer(ctx)
				}
			}(task)
		}
		workers.Wait()
	} else {
		failDownloadTasks(tasks, reserveErr)
	}
	if c != nil {
		c.noteWaveFinished(tasks)
		c.gate.RUnlock()
	}
	c.observeWave(stageObserver, waveStarted)
	for _, task := range tasks {
		runDownloadTaskAfter(task)
	}
	return reserveErr
}

func (c *downloadCoordinator) noteWaveStarted(tasks []*mediaDownloadTask, queuedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, task := range tasks {
		c.metrics.ActiveSlots += downloadTaskSlots(task)
		c.metrics.QueueWaitSeconds += time.Since(queuedAt).Seconds()
	}
	c.activeFiles += len(tasks)
	if c.metrics.ActiveSlots > c.metrics.PeakActiveSlots {
		c.metrics.PeakActiveSlots = c.metrics.ActiveSlots
	}
	if c.activeFiles > c.metrics.PeakActiveFiles {
		c.metrics.PeakActiveFiles = c.activeFiles
	}
}

func (c *downloadCoordinator) noteWaveFinished(tasks []*mediaDownloadTask) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, task := range tasks {
		c.metrics.ActiveSlots -= downloadTaskSlots(task)
	}
	c.activeFiles -= len(tasks)
}

func (c *downloadCoordinator) observeWave(stageObserver stages.Observer, startedAt time.Time) {
	wall := time.Since(startedAt)
	stages.ObserveSince(stageObserver, stages.Download, startedAt)
	if c == nil {
		return
	}
	c.mu.Lock()
	c.metrics.WallSeconds += wall.Seconds()
	c.mu.Unlock()
}

func runDownloadTaskAfter(task *mediaDownloadTask) {
	if task != nil && task.After != nil {
		task.After()
	}
}

func (c *downloadCoordinator) reserveWave(ctx context.Context) error {
	if c == nil || c.reserve == nil {
		return nil
	}
	return c.reserve(ctx)
}

func failDownloadTasks(tasks []*mediaDownloadTask, err error) {
	for _, task := range tasks {
		if task != nil && task.Fail != nil {
			task.Fail(err)
		}
	}
}

func (c *downloadCoordinator) finish() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return
	}
	c.finished = true
	metrics := c.metrics
	c.mu.Unlock()
	if c.observer != nil {
		c.observer(stages.DownloadTransferMetrics{Coordinator: &metrics})
	}
}

func (c *downloadCoordinator) snapshot() stages.DownloadCoordinatorMetrics {
	if c == nil {
		return stages.DownloadCoordinatorMetrics{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.metrics
}

func downloadTaskSlots(task *mediaDownloadTask) int {
	if task != nil && task.Slots >= downloadCoordinatorSlots {
		return downloadCoordinatorSlots
	}
	return 1
}

func cancelDownloadTasks(tasks []*mediaDownloadTask, err error) {
	for _, task := range tasks {
		if task != nil && task.Cancel != nil {
			task.Cancel(err)
		}
	}
}
