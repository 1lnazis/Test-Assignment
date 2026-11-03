package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/domain"
	"github.com/shopspring/decimal"
)

type fakeQueue struct {
	claim    func(context.Context, int, time.Duration) (Job, bool, error)
	complete func(context.Context, Job, decimal.Decimal, string) (bool, error)
	retry    func(context.Context, Job, string, time.Duration) (bool, error)
	fail     func(context.Context, Job, string) (bool, error)
}

func (q fakeQueue) ClaimNext(ctx context.Context, max int, lease time.Duration) (Job, bool, error) {
	if q.claim == nil {
		return Job{}, false, nil
	}
	return q.claim(ctx, max, lease)
}
func (q fakeQueue) Complete(ctx context.Context, j Job, p decimal.Decimal, name string) (bool, error) {
	if q.complete == nil {
		return false, errors.New("unexpected Complete")
	}
	return q.complete(ctx, j, p, name)
}
func (q fakeQueue) Retry(ctx context.Context, j Job, reason string, d time.Duration) (bool, error) {
	if q.retry == nil {
		return false, errors.New("unexpected Retry")
	}
	return q.retry(ctx, j, reason, d)
}
func (q fakeQueue) Fail(ctx context.Context, j Job, reason string) (bool, error) {
	if q.fail == nil {
		return false, errors.New("unexpected Fail")
	}
	return q.fail(ctx, j, reason)
}

type fakeQuoter func(context.Context, domain.Pair) (decimal.Decimal, error)

func (f fakeQuoter) Fetch(ctx context.Context, p domain.Pair) (decimal.Decimal, error) {
	return f(ctx, p)
}
func (fakeQuoter) Name() string { return "test-provider" }

type temporaryError bool

func (e temporaryError) Error() string   { return "provider unavailable" }
func (e temporaryError) Retryable() bool { return bool(e) }

func testWorker(t *testing.T, q Queue, f fakeQuoter) *Worker {
	t.Helper()
	w, err := New(q, f, slog.New(slog.NewTextHandler(io.Discard, nil)), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func jobForTest(attempt int) Job {
	return Job{ID: "job", Pair: domain.Pair{Base: "EUR", Quote: "MXN"}, Attempts: attempt}
}

func TestWorker_DrainsReadyJobsWithoutWaiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	claimed, saved := 0, 0
	q := fakeQueue{
		claim: func(ctx context.Context, max int, lease time.Duration) (Job, bool, error) {
			if max != 3 || lease != 30*time.Second {
				t.Errorf("claim settings: %d %v", max, lease)
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Error("claim has no deadline")
			}
			claimed++
			return jobForTest(1), true, nil
		},
		complete: func(_ context.Context, j Job, p decimal.Decimal, name string) (bool, error) {
			if p.String() != "21.072" || name != "test-provider" {
				t.Errorf("result: %v %s", p, name)
			}
			saved++
			if saved == 3 {
				cancel()
			}
			return true, nil
		},
	}
	w := testWorker(t, q, func(ctx context.Context, p domain.Pair) (decimal.Decimal, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("fetch has no deadline")
		}
		return decimal.RequireFromString("21.072"), nil
	})
	w.wait = func(context.Context, time.Duration) error { return errors.New("unexpected idle wait") }
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if claimed != 3 || saved != 3 {
		t.Fatalf("claimed=%d saved=%d", claimed, saved)
	}
}

func TestWorker_FailurePolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		attempt int
		err     error
		retry   bool
		delay   time.Duration
	}{
		{"first transient", 1, temporaryError(true), true, time.Second},
		{"second transient", 2, temporaryError(true), true, 2 * time.Second},
		{"last transient", 3, temporaryError(true), false, 0},
		{"permanent HTTP", 1, temporaryError(false), false, 0},
		{"unknown payload", 1, errors.New("invalid JSON"), false, 0},
		{"fetch timeout", 1, fmt.Errorf("fetch: %w", context.DeadlineExceeded), true, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			writes := 0
			q := fakeQueue{claim: func(context.Context, int, time.Duration) (Job, bool, error) { return jobForTest(tc.attempt), true, nil }}
			q.retry = func(_ context.Context, j Job, reason string, d time.Duration) (bool, error) {
				writes++
				if !tc.retry || d != tc.delay || j.Attempts != tc.attempt || reason != tc.err.Error() {
					t.Errorf("unexpected retry: %+v %s %v", j, reason, d)
				}
				cancel()
				return true, nil
			}
			q.fail = func(_ context.Context, j Job, reason string) (bool, error) {
				writes++
				if tc.retry || reason != tc.err.Error() {
					t.Errorf("unexpected fail: %s", reason)
				}
				cancel()
				return true, nil
			}
			w := testWorker(t, q, func(context.Context, domain.Pair) (decimal.Decimal, error) { return decimal.Zero, tc.err })
			if err := w.Run(ctx); err != nil {
				t.Fatal(err)
			}
			if writes != 1 {
				t.Fatalf("writes=%d", writes)
			}
		})
	}
}

func TestWorker_ApplicationCancellationLeavesLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q := fakeQueue{claim: func(context.Context, int, time.Duration) (Job, bool, error) { return jobForTest(1), true, nil }}
	w := testWorker(t, q, func(context.Context, domain.Pair) (decimal.Decimal, error) {
		cancel()
		return decimal.Zero, context.Canceled
	})
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	} // Любая попытка записи вернула бы unexpected ...
}

func TestWorker_ObtainedPriceSavedAfterCancellation(t *testing.T) {
	type key struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "trace"))
	defer cancel()
	saved := false
	q := fakeQueue{
		claim: func(context.Context, int, time.Duration) (Job, bool, error) { return jobForTest(1), true, nil },
		complete: func(ctx context.Context, _ Job, _ decimal.Decimal, _ string) (bool, error) {
			if ctx.Err() != nil || ctx.Value(key{}) != "trace" {
				t.Errorf("save context: %v", ctx.Err())
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > DefaultOptions().DBTimeout {
				t.Error("save must have short deadline")
			}
			saved = true
			return true, nil
		},
	}
	w := testWorker(t, q, func(context.Context, domain.Pair) (decimal.Decimal, error) {
		cancel()
		return decimal.NewFromInt(21), nil
	})
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !saved {
		t.Fatal("price lost")
	}
}

func TestWorker_PanicDoesNotKillLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	claims, fetches, saves := 0, 0, 0
	q := fakeQueue{
		claim: func(context.Context, int, time.Duration) (Job, bool, error) {
			claims++
			return jobForTest(1), true, nil
		},
		complete: func(context.Context, Job, decimal.Decimal, string) (bool, error) { saves++; cancel(); return true, nil },
	}
	w := testWorker(t, q, func(context.Context, domain.Pair) (decimal.Decimal, error) {
		fetches++
		if fetches == 1 {
			panic("broken provider")
		}
		return decimal.NewFromInt(21), nil
	})
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if claims != 2 || fetches != 2 || saves != 1 {
		t.Fatalf("claims=%d fetches=%d saves=%d", claims, fetches, saves)
	}
}

func TestWorker_EmptyOrUnavailableQueueWaits(t *testing.T) {
	for _, claimErr := range []error{nil, errors.New("database unavailable")} {
		t.Run(fmt.Sprint(claimErr), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			claims, waits := 0, 0
			q := fakeQueue{claim: func(context.Context, int, time.Duration) (Job, bool, error) { claims++; return Job{}, false, claimErr }}
			w := testWorker(t, q, func(context.Context, domain.Pair) (decimal.Decimal, error) {
				return decimal.Zero, errors.New("unexpected fetch")
			})
			w.wait = func(_ context.Context, d time.Duration) error {
				waits++
				if d != time.Second {
					t.Errorf("wait=%v", d)
				}
				cancel()
				return context.Canceled
			}
			if err := w.Run(ctx); err != nil {
				t.Fatal(err)
			}
			if claims != 1 || waits != 1 {
				t.Fatalf("claims=%d waits=%d", claims, waits)
			}
		})
	}
}

func TestWorker_StaleResultIsNotFatal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := fakeQueue{
		claim:    func(context.Context, int, time.Duration) (Job, bool, error) { return jobForTest(1), true, nil },
		complete: func(context.Context, Job, decimal.Decimal, string) (bool, error) { cancel(); return false, nil },
	}
	w := testWorker(t, q, func(context.Context, domain.Pair) (decimal.Decimal, error) { return decimal.NewFromInt(21), nil })
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWorker_SaveFailureCancelsAndJoinsAllWorkers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	otherStarted := make(chan struct{})
	var claims atomic.Int32
	var otherStopped atomic.Bool
	want := errors.New("database write failed")
	q := fakeQueue{
		claim: func(ctx context.Context, _ int, _ time.Duration) (Job, bool, error) {
			if claims.Add(1) == 1 {
				return jobForTest(1), true, nil
			}
			close(otherStarted)
			<-ctx.Done()
			otherStopped.Store(true)
			return Job{}, false, ctx.Err()
		},
		complete: func(ctx context.Context, _ Job, _ decimal.Decimal, _ string) (bool, error) {
			select {
			case <-otherStarted:
				return false, want
			case <-ctx.Done():
				return false, ctx.Err()
			}
		},
	}
	w := testWorker(t, q, func(context.Context, domain.Pair) (decimal.Decimal, error) { return decimal.NewFromInt(21), nil })
	w.opts.Count = 2
	if err := w.Run(ctx); !errors.Is(err, want) {
		t.Fatalf("Run=%v", err)
	}
	if !otherStopped.Load() {
		t.Fatal("Run returned before other worker stopped")
	}
}

func TestWorker_InvalidProviderPriceFailsPermanently(t *testing.T) {
	for _, raw := range []string{"0", "-1", "0.00000000001", "99999999999999.99999999995"} {
		t.Run(raw, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failed := false
			q := fakeQueue{
				claim: func(context.Context, int, time.Duration) (Job, bool, error) { return jobForTest(1), true, nil },
				fail:  func(context.Context, Job, string) (bool, error) { failed = true; cancel(); return true, nil },
			}
			w := testWorker(t, q, func(context.Context, domain.Pair) (decimal.Decimal, error) {
				return decimal.RequireFromString(raw), nil
			})
			if err := w.Run(ctx); err != nil {
				t.Fatal(err)
			}
			if !failed {
				t.Fatal("invalid price was not rejected")
			}
		})
	}
}

func TestRetryClassificationAndBackoff(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{temporaryError(true), true}, {temporaryError(false), false},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), true},
		{context.Canceled, false}, {io.ErrUnexpectedEOF, true}, {errors.New("bad payload"), false},
		{&net.DNSError{Err: "lookup failed", Name: "example.test"}, true},
	} {
		if got := isRetryable(tc.err); got != tc.want {
			t.Errorf("isRetryable(%v)=%v", tc.err, got)
		}
	}
	w := &Worker{opts: DefaultOptions()}
	for _, tc := range []struct {
		attempt int
		want    time.Duration
	}{{1, time.Second}, {2, 2 * time.Second}, {3, 4 * time.Second}, {5, 16 * time.Second}, {6, 30 * time.Second}, {1000000, 30 * time.Second}} {
		if got := w.retryDelay(tc.attempt); got != tc.want {
			t.Errorf("delay(%d)=%v", tc.attempt, got)
		}
	}
	w.opts.RetryBaseDelay = time.Duration(1 << 62)
	w.opts.RetryMaxDelay = time.Duration(1<<63 - 1)
	if got := w.retryDelay(2); got != w.opts.RetryMaxDelay {
		t.Errorf("overflowed delay: %v", got)
	}
}

func TestOptionsValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Options)
	}{
		{"count", func(o *Options) { o.Count = 0 }},
		{"attempts", func(o *Options) { o.MaxAttempts = 0 }},
		{"attempt overflow", func(o *Options) { o.MaxAttempts = 1 << 31 }},
		{"poll", func(o *Options) { o.PollInterval = 0 }},
		{"fetch", func(o *Options) { o.FetchTimeout = -1 }},
		{"database", func(o *Options) { o.DBTimeout = 0 }},
		{"base delay", func(o *Options) { o.RetryBaseDelay = time.Nanosecond }},
		{"delay cap", func(o *Options) { o.RetryMaxDelay = o.RetryBaseDelay - 1 }},
		{"lease", func(o *Options) { o.Lease = o.FetchTimeout + 2*o.DBTimeout }},
		{"sum overflow", func(o *Options) { o.FetchTimeout = time.Duration(1<<63 - 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := DefaultOptions()
			tc.change(&o)
			if err := o.Validate(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if err := DefaultOptions().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWaitContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait=%v", err)
	}
}
