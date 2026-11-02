package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/domain"
	"github.com/1lnazis/Test-Assignment/internal/worker"
)

func TestRepo_Retry_SchedulesByDatabaseTime(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	ctx := context.Background()
	created, err := repo.CreateUpdate(ctx, domain.Pair{Base: "EUR", Quote: "MXN"})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := repo.ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found {
		t.Fatalf("claim: %v %v", found, err)
	}
	var before, after, next time.Time
	if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if saved, err := repo.Retry(ctx, job, "HTTP 503", time.Hour); err != nil || !saved {
		t.Fatalf("Retry: %v %v", saved, err)
	}
	if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var clean bool
	if err := pool.QueryRow(ctx, `SELECT attempts,next_attempt_at,locked_until IS NULL AND price IS NULL AND provider IS NULL FROM quote_updates WHERE id=$1`, created.ID).Scan(&attempts, &next, &clean); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !clean || next.Before(before.Add(time.Hour)) || next.After(after.Add(time.Hour)) {
		t.Fatalf("attempts=%d clean=%v next=%v", attempts, clean, next)
	}
	got, err := repo.GetUpdate(ctx, created.ID)
	if err != nil || got.Status != domain.StatusPending || got.Error != "HTTP 503" {
		t.Fatalf("update=%+v err=%v", got, err)
	}
	if _, found, err := repo.ClaimNext(ctx, 3, time.Hour); err != nil || found {
		t.Fatalf("early claim: %v %v", found, err)
	}
	queueExec(t, pool, `UPDATE quote_updates SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, job.ID)
	nextJob, found, err := repo.ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found || nextJob.ID != job.ID || nextJob.Attempts != 2 {
		t.Fatalf("next claim=%+v found=%v err=%v", nextJob, found, err)
	}
}

func TestRepo_Fail_IsTerminalAndReleasesLease(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	ctx := context.Background()
	if _, err := repo.CreateUpdate(ctx, domain.Pair{Base: "EUR", Quote: "MXN"}); err != nil {
		t.Fatal(err)
	}
	job, found, err := repo.ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found {
		t.Fatalf("claim: %v %v", found, err)
	}
	if saved, err := repo.Fail(ctx, job, "unsupported pair"); err != nil || !saved {
		t.Fatalf("Fail: %v %v", saved, err)
	}
	got, err := repo.GetUpdate(ctx, job.ID)
	if err != nil || got.Status != domain.StatusFailed || got.Error != "unsupported pair" || got.Price != nil {
		t.Fatalf("update=%+v err=%v", got, err)
	}
	var clean bool
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT locked_until IS NULL AND price IS NULL AND provider IS NULL, attempts FROM quote_updates WHERE id=$1`, job.ID).Scan(&clean, &attempts); err != nil {
		t.Fatal(err)
	}
	if !clean || attempts != 1 {
		t.Fatalf("clean=%v attempts=%d", clean, attempts)
	}
	if _, found, err := repo.ClaimNext(ctx, 3, time.Hour); err != nil || found {
		t.Fatalf("terminal claim: %v %v", found, err)
	}
	if saved, err := repo.Retry(ctx, job, "late", 0); err != nil || saved {
		t.Fatalf("terminal retry: %v %v", saved, err)
	}
	if saved, err := repo.Fail(ctx, job, "late"); err != nil || saved {
		t.Fatalf("terminal fail: %v %v", saved, err)
	}
}

func TestRepo_Failure_StaleAttemptIsRejected(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	ctx := context.Background()
	if _, err := repo.CreateUpdate(ctx, domain.Pair{Base: "EUR", Quote: "MXN"}); err != nil {
		t.Fatal(err)
	}
	old, found, err := repo.ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found {
		t.Fatalf("claim: %v %v", found, err)
	}
	queueExec(t, pool, `UPDATE quote_updates SET locked_until=now()-interval '1 second' WHERE id=$1`, old.ID)
	current, found, err := repo.ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found || current.Attempts != 2 {
		t.Fatalf("reclaim: %+v %v %v", current, found, err)
	}
	if saved, err := repo.Retry(ctx, old, "stale retry", 0); err != nil || saved {
		t.Fatalf("stale retry: %v %v", saved, err)
	}
	if saved, err := repo.Fail(ctx, old, "stale failure"); err != nil || saved {
		t.Fatalf("stale fail: %v %v", saved, err)
	}
	got, err := repo.GetUpdate(ctx, current.ID)
	if err != nil || got.Status != domain.StatusProcessing || got.Error != "" {
		t.Fatalf("current=%+v err=%v", got, err)
	}
	if saved, err := repo.Retry(ctx, current, "current retry", 0); err != nil || !saved {
		t.Fatalf("current retry: %v %v", saved, err)
	}
}

func TestRepo_Failure_InvalidInput(t *testing.T) {
	repo := NewRepo(nil)
	for _, tc := range []struct {
		attempt int
		reason  string
	}{{0, "error"}, {-1, "error"}, {1, "  "}} {
		job := worker.Job{Attempts: tc.attempt}
		if saved, err := repo.Retry(context.Background(), job, tc.reason, time.Second); err == nil || saved {
			t.Fatalf("Retry: %v %v", saved, err)
		}
		if saved, err := repo.Fail(context.Background(), job, tc.reason); err == nil || saved {
			t.Fatalf("Fail: %v %v", saved, err)
		}
	}
	for _, delay := range []time.Duration{-time.Second, time.Nanosecond} {
		if saved, err := repo.Retry(context.Background(), worker.Job{Attempts: 1}, "error", delay); err == nil || saved {
			t.Fatalf("Retry delay %v: %v %v", delay, saved, err)
		}
	}
}

func TestRepo_Failure_CancellationAndDatabaseError(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	job := worker.Job{ID: "00000000-0000-4000-8000-000000000001", Attempts: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if saved, err := repo.Retry(ctx, job, "error", 0); saved || !errors.Is(err, context.Canceled) {
		t.Fatalf("Retry: %v %v", saved, err)
	}
	if saved, err := repo.Fail(ctx, job, "error"); saved || !errors.Is(err, context.Canceled) {
		t.Fatalf("Fail: %v %v", saved, err)
	}
	pool.Close()
	if saved, err := repo.Retry(context.Background(), job, "error", 0); saved || err == nil {
		t.Fatalf("Retry closed: %v %v", saved, err)
	}
	if saved, err := repo.Fail(context.Background(), job, "error"); saved || err == nil {
		t.Fatalf("Fail closed: %v %v", saved, err)
	}
}
