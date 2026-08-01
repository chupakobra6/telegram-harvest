package mtproto

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadCoordinatorPairsSmallJobsAndKeepsLargeJobFair(t *testing.T) {
	coordinator := newDownloadCoordinator(nil, nil)
	var mu sync.Mutex
	var events []string
	task := func(name string, slots int) *mediaDownloadTask {
		return &mediaDownloadTask{Slots: slots, Transfer: func(context.Context) {
			mu.Lock()
			events = append(events, "start:"+name)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			events = append(events, "done:"+name)
			mu.Unlock()
		}}
	}
	tasks := []*mediaDownloadTask{
		task("small-a", 1),
		task("small-b", 1),
		task("large", 2),
		task("small-c", 1),
	}
	if err := coordinator.runBatch(t.Context(), tasks, nil); err != nil {
		t.Fatal(err)
	}
	metrics := coordinator.snapshot()
	if metrics.PeakActiveSlots != 2 || metrics.PeakActiveFiles != 2 || metrics.SmallParallelPairs != 1 {
		t.Fatalf("metrics = %+v", metrics)
	}
	largeStart := eventIndex(events, "start:large")
	if largeStart < eventIndex(events, "done:small-a") || largeStart < eventIndex(events, "done:small-b") {
		t.Fatalf("large job started before the preceding FIFO pair completed: %v", events)
	}
	if eventIndex(events, "start:small-c") < eventIndex(events, "done:large") {
		t.Fatalf("small job bypassed the large job: %v", events)
	}
}

func TestDownloadCoordinatorRunsTwoSmallTransfersConcurrently(t *testing.T) {
	coordinator := newDownloadCoordinator(nil, nil)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	batchDone := make(chan error, 1)
	task := func() *mediaDownloadTask {
		return &mediaDownloadTask{Slots: 1, Transfer: func(context.Context) {
			started <- struct{}{}
			<-release
		}}
	}
	go func() {
		batchDone <- coordinator.runBatch(t.Context(), []*mediaDownloadTask{task(), task()}, nil)
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("small transfers did not start concurrently")
		}
	}
	close(release)
	if err := <-batchDone; err != nil {
		t.Fatal(err)
	}
	metrics := coordinator.snapshot()
	if metrics.PeakActiveSlots != 2 || metrics.PeakActiveFiles != 2 {
		t.Fatalf("metrics = %+v", metrics)
	}
}

func TestDownloadCoordinatorMakesHistoryExclusive(t *testing.T) {
	coordinator := newDownloadCoordinator(nil, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	batchDone := make(chan error, 1)
	go func() {
		batchDone <- coordinator.runBatch(t.Context(), []*mediaDownloadTask{{Slots: 1, Transfer: func(context.Context) {
			close(started)
			<-release
		}}}, nil)
	}()
	<-started

	historyEntered := make(chan struct{})
	historyDone := make(chan error, 1)
	go func() {
		historyDone <- coordinator.runHistory(func() error {
			close(historyEntered)
			return nil
		})
	}()
	select {
	case <-historyEntered:
		t.Fatal("history entered while a download was active")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-batchDone; err != nil {
		t.Fatal(err)
	}
	if err := <-historyDone; err != nil {
		t.Fatal(err)
	}
	metrics := coordinator.snapshot()
	if metrics.HistorySections != 1 || metrics.HistoryDownloadOverlap != 0 || metrics.ActiveSlots != 0 {
		t.Fatalf("metrics = %+v", metrics)
	}
}

func TestDownloadCoordinatorCancellationCancelsUnstartedTasks(t *testing.T) {
	coordinator := newDownloadCoordinator(nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var runs atomic.Int32
	var cancellations atomic.Int32
	tasks := make([]*mediaDownloadTask, 3)
	for index := range tasks {
		tasks[index] = &mediaDownloadTask{
			Slots:    1,
			Transfer: func(context.Context) { runs.Add(1) },
			Cancel: func(error) {
				cancellations.Add(1)
			},
		}
	}
	if err := coordinator.runBatch(ctx, tasks, nil); err == nil {
		t.Fatal("expected cancellation")
	}
	if runs.Load() != 0 || cancellations.Load() != 3 {
		t.Fatalf("runs=%d cancellations=%d", runs.Load(), cancellations.Load())
	}
}

func TestDownloadCoordinatorSerializesConcurrentBatchesGlobally(t *testing.T) {
	coordinator := newDownloadCoordinator(nil, nil)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		firstDone <- coordinator.runBatch(t.Context(), []*mediaDownloadTask{{Slots: 2, Transfer: func(context.Context) {
			close(firstStarted)
			<-releaseFirst
		}}}, nil)
	}()
	<-firstStarted
	go func() {
		secondDone <- coordinator.runBatch(t.Context(), []*mediaDownloadTask{{Slots: 1, Transfer: func(context.Context) {
			close(secondStarted)
		}}}, nil)
	}()
	select {
	case <-secondStarted:
		t.Fatal("second batch bypassed the active global batch")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if metrics := coordinator.snapshot(); metrics.PeakActiveSlots != 2 || metrics.HistoryDownloadOverlap != 0 {
		t.Fatalf("metrics = %+v", metrics)
	}
}

func TestDownloadCoordinatorCancelsBatchWaitingForGlobalTurn(t *testing.T) {
	coordinator := newDownloadCoordinator(nil, nil)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- coordinator.runBatch(t.Context(), []*mediaDownloadTask{{Slots: 2, Transfer: func(context.Context) {
			close(firstStarted)
			<-releaseFirst
		}}}, nil)
	}()
	<-firstStarted

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var canceled atomic.Int32
	err := coordinator.runBatch(ctx, []*mediaDownloadTask{{Slots: 1, Cancel: func(error) { canceled.Add(1) }}}, nil)
	if err == nil || canceled.Load() != 1 {
		t.Fatalf("err=%v canceled=%d", err, canceled.Load())
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestDownloadCoordinatorReservesOncePerWave(t *testing.T) {
	var reservations atomic.Int32
	coordinator := newDownloadCoordinator(nil, func(context.Context) error {
		reservations.Add(1)
		return nil
	})
	tasks := []*mediaDownloadTask{
		{Slots: 1, Transfer: func(context.Context) {}},
		{Slots: 1, Transfer: func(context.Context) {}},
		{Slots: 2, Transfer: func(context.Context) {}},
	}
	if err := coordinator.runBatch(t.Context(), tasks, nil); err != nil {
		t.Fatal(err)
	}
	if got := reservations.Load(); got != 2 {
		t.Fatalf("reservations = %d, want one for the small pair and one for the large file", got)
	}
}

func eventIndex(events []string, target string) int {
	for index, event := range events {
		if event == target {
			return index
		}
	}
	return -1
}
