package charging

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type RetryTask struct {
	Key      string
	Action   string
	Attempts int
	Execute  func(context.Context) error
}

type RetryQueue struct {
	logger      *slog.Logger
	maxAttempts int
	baseDelay   time.Duration

	mu         sync.Mutex
	scheduled  map[string]struct{}
	deadLetter []RetryTask
}

func NewRetryQueue(logger *slog.Logger, maxAttempts int, baseDelay time.Duration) *RetryQueue {
	if logger == nil {
		logger = slog.Default()
	}
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if baseDelay <= 0 {
		baseDelay = time.Second
	}
	return &RetryQueue{
		logger:      logger.With("component", "retry_queue"),
		maxAttempts: maxAttempts,
		baseDelay:   baseDelay,
		scheduled:   make(map[string]struct{}),
	}
}

func (q *RetryQueue) Enqueue(task RetryTask) {
	q.mu.Lock()
	if _, exists := q.scheduled[task.Key]; exists {
		q.mu.Unlock()
		return
	}
	q.scheduled[task.Key] = struct{}{}
	q.mu.Unlock()

	go q.run(task)
}

func (q *RetryQueue) DeadLetters() []RetryTask {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]RetryTask, len(q.deadLetter))
	copy(out, q.deadLetter)
	return out
}

func (q *RetryQueue) run(task RetryTask) {
	defer func() {
		q.mu.Lock()
		delete(q.scheduled, task.Key)
		q.mu.Unlock()
	}()

	for attempt := 1; attempt <= q.maxAttempts; attempt++ {
		task.Attempts = attempt
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := task.Execute(ctx)
		cancel()
		if err == nil {
			q.logger.Info("retry task succeeded", "action", task.Action, "key", task.Key, "attempt", attempt)
			return
		}

		q.logger.Warn("retry task failed", "action", task.Action, "key", task.Key, "attempt", attempt, "error", err)
		if attempt == q.maxAttempts {
			q.mu.Lock()
			q.deadLetter = append(q.deadLetter, task)
			q.mu.Unlock()
			return
		}

		delay := q.baseDelay * time.Duration(1<<(attempt-1))
		time.Sleep(delay)
	}
}
