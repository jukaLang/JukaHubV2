package main

import (
	"context"
	"log"
	"sync"
	"time"
)

// task represents a unit of work that can be executed.
type task struct {
	fn   func(ctx context.Context) error
	ctx  context.Context
	done chan error
}

// TaskGroup manages a bounded pool of workers and a queue of tasks.
// It is safe for concurrent use and supports cancellation via context.
type TaskGroup struct {
	mu       sync.Mutex
	wg       sync.WaitGroup
	tasks    chan task
	shutdown chan struct{}
	closed   bool
}

// NewTaskGroup creates a TaskGroup with the specified worker count and queue size.
func NewTaskGroup(workers, queueSize int) *TaskGroup {
	tg := &TaskGroup{
		tasks:    make(chan task, queueSize),
		shutdown: make(chan struct{}),
	}
	for i := 0; i < workers; i++ {
		tg.wg.Add(1)
		go tg.worker()
	}
	return tg
}

// Submit enqueues a task for execution. If the queue is full, the call blocks.
// The returned channel receives the task result (or nil if the group is shut down).
func (tg *TaskGroup) Submit(ctx context.Context, fn func(ctx context.Context) error) <-chan error {
	if tg.isClosed() {
		ch := make(chan error, 1)
		ch <- context.Canceled
		return ch
	}
	ch := make(chan error, 1)
	select {
	case tg.tasks <- task{fn: fn, ctx: ctx, done: ch}:
		return ch
	case <-ctx.Done():
		ch <- ctx.Err()
		return ch
	}
}

// SubmitWait enqueues a task and blocks until it completes.
func (tg *TaskGroup) SubmitWait(ctx context.Context, fn func(ctx context.Context) error) error {
	ch := tg.Submit(ctx, fn)
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Shutdown gracefully stops the task group: it stops accepting new tasks and
// waits for all currently executing tasks to finish.
func (tg *TaskGroup) Shutdown() {
	tg.mu.Lock()
	if tg.closed {
		tg.mu.Unlock()
		return
	}
	tg.closed = true
	close(tg.shutdown)
	tg.mu.Unlock()
	tg.wg.Wait()
}

// Close is an alias for Shutdown for compatibility.
func (tg *TaskGroup) Close() { tg.Shutdown() }

func (tg *TaskGroup) isClosed() bool {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	return tg.closed
}

func (tg *TaskGroup) worker() {
	defer tg.wg.Done()
	for {
		select {
		case t, ok := <-tg.tasks:
			if !ok {
				return
			}
			err := t.fn(t.ctx)
			select {
			case t.done <- err:
			default:
			}
		case <-tg.shutdown:
			return
		}
	}
}

// --- Global task runner ---

var (
	globalTaskGroup     *TaskGroup
	globalTaskGroupOnce sync.Once
)

// GlobalTasks returns the global shared TaskGroup. It is initialized on first
// use with conservative defaults suitable for both TSP and Windows.
func GlobalTasks() *TaskGroup {
	globalTaskGroupOnce.Do(func() {
		// Trimui: 2 image workers, 3 network workers.
		// Windows: 4 image workers, 6 network workers.
		workers := 2
		if IsWindows() {
			workers = 4
		}
		globalTaskGroup = NewTaskGroup(workers, workers*4)
	})
	return globalTaskGroup
}

// SubmitTask submits a task to the global pool.
func SubmitTask(ctx context.Context, fn func(ctx context.Context) error) <-chan error {
	return GlobalTasks().Submit(ctx, fn)
}

// ShutdownGlobalTasks stops the global task pool. Call during app shutdown.
func ShutdownGlobalTasks() {
	if globalTaskGroup != nil {
		globalTaskGroup.Shutdown()
	}
}

// --- Context helpers ---

// CancelContext returns a cancellable context derived from the background context,
// with an optional timeout. If timeoutMs <= 0, no timeout is applied.
func CancelContext(timeoutMs int64) (context.Context, context.CancelFunc) {
	if timeoutMs <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), msToDuration(timeoutMs))
}

func msToDuration(ms int64) time.Duration {
	// Convert milliseconds to a time.Duration for context.WithTimeout.
	return time.Duration(ms) * time.Millisecond
}

// LoggedTask wraps a task function with structured logging.
func LoggedTask(op string, fn func(ctx context.Context) error) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		log.Printf("[task] start: %s", op)
		err := fn(ctx)
		if err != nil {
			log.Printf("[task] error: %s: %v", op, err)
		} else {
			log.Printf("[task] done: %s", op)
		}
		return err
	}
}
