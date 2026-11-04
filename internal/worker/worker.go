package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime/debug"
	"time"

	"github.com/shopspring/decimal"
)

type Worker struct {
	queue  Queue
	quoter Quoter
	log    *slog.Logger
	opts   Options
	wait   func(context.Context, time.Duration) error
}

func New(queue Queue, quoter Quoter, log *slog.Logger, opts Options) (*Worker, error) {
	if queue == nil || quoter == nil || log == nil {
		return nil, fmt.Errorf("worker dependencies must not be nil")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	return &Worker{queue: queue, quoter: quoter, log: log, opts: opts, wait: waitContext}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, w.opts.Count)
	for slot := range w.opts.Count {
		go func() { finished <- w.loop(runCtx, slot) }()
	}
	var firstErr error
	for range w.opts.Count {
		if err := <-finished; err != nil && firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	return firstErr
}

func (w *Worker) loop(ctx context.Context, slot int) error {
	for ctx.Err() == nil {
		worked, err := w.step(ctx, slot)
		if err != nil {
			return err
		}
		if !worked {
			if err := w.wait(ctx, w.opts.PollInterval); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("worker wait: %w", err)
			}
		}
	}
	return nil
}

func (w *Worker) step(ctx context.Context, slot int) (worked bool, err error) {
	var job Job
	defer func() {
		if value := recover(); value != nil {
			w.log.Error("worker panic", "worker", slot, "job_id", job.ID,
				"panic", value, "stack", string(debug.Stack()))
			err = nil
		}
	}()

	claimCtx, cancelClaim := context.WithTimeout(ctx, w.opts.DBTimeout)
	defer cancelClaim()
	var found bool
	job, found, err = w.queue.ClaimNext(claimCtx, w.opts.MaxAttempts, w.opts.Lease)
	cancelClaim()
	if err != nil {
		if ctx.Err() == nil {
			w.log.Error("claim failed", "worker", slot, "error", err)
		}
		return false, nil
	}
	if !found {
		return false, nil
	}
	worked = true
	if ctx.Err() != nil {
		return true, nil
	}

	started := time.Now()
	log := w.log.With("worker", slot, "job_id", job.ID,
		"pair", job.Pair.String(), "attempt", job.Attempts)
	log.Debug("job claimed")

	fetchCtx, cancelFetch := context.WithTimeout(ctx, w.opts.FetchTimeout)
	defer cancelFetch()
	price, fetchErr := w.quoter.Fetch(fetchCtx, job.Pair)
	cancelFetch()
	if fetchErr == nil {
		rounded := price.Round(10)
		if !rounded.IsPositive() || rounded.GreaterThanOrEqual(decimal.New(1, 14)) {
			fetchErr = fmt.Errorf("provider price does not fit positive numeric(24,10)")
		} else {

			return true, w.persist(ctx, job, "complete", true, func(saveCtx context.Context) (bool, error) {
				provider := w.quoter.Name()
				saved, err := w.queue.Complete(saveCtx, job, price, provider)
				if err == nil && saved {
					log.Info("job completed", "provider", provider,
						"duration_ms", time.Since(started).Milliseconds())
				}
				return saved, err
			})
		}
	}
	if ctx.Err() != nil {
		return true, nil
	}
	if job.Attempts < w.opts.MaxAttempts && isRetryable(fetchErr) {
		delay := w.retryDelay(job.Attempts)
		return true, w.persist(ctx, job, "retry", false, func(saveCtx context.Context) (bool, error) {
			saved, err := w.queue.Retry(saveCtx, job, fetchErr.Error(), delay)
			if err == nil && saved {
				log.Warn("job retry scheduled", "error", fetchErr,
					"retry_delay", delay.String(), "duration_ms", time.Since(started).Milliseconds())
			}
			return saved, err
		})
	}
	return true, w.persist(ctx, job, "fail", false, func(saveCtx context.Context) (bool, error) {
		saved, err := w.queue.Fail(saveCtx, job, fetchErr.Error())
		if err == nil && saved {
			log.Error("job failed", "error", fetchErr,
				"duration_ms", time.Since(started).Milliseconds())
		}
		return saved, err
	})
}

func (w *Worker) persist(ctx context.Context, job Job, action string, detach bool, write func(context.Context) (bool, error)) error {
	base := ctx
	if detach {
		base = context.WithoutCancel(ctx)
	}
	saveCtx, cancel := context.WithTimeout(base, w.opts.DBTimeout)
	defer cancel()
	saved, err := write(saveCtx)
	if err != nil {
		if !detach && ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("job %s %s: %w", job.ID, action, err)
	}
	if !saved {
		w.log.Warn("stale job result ignored", "job_id", job.ID, "attempt", job.Attempts, "action", action)
	}
	return nil
}

func isRetryable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var classified interface{ Retryable() bool }
	if errors.As(err, &classified) {
		return classified.Retryable()
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func (w *Worker) retryDelay(attempt int) time.Duration {
	delay := w.opts.RetryBaseDelay
	for n := 1; n < attempt && delay < w.opts.RetryMaxDelay; n++ {
		if delay > w.opts.RetryMaxDelay/2 {
			return w.opts.RetryMaxDelay
		}
		delay *= 2
	}
	if delay > w.opts.RetryMaxDelay {
		return w.opts.RetryMaxDelay
	}
	return delay
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
