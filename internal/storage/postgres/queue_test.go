package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/1lnazis/Test-Assignment/internal/domain"
	"github.com/1lnazis/Test-Assignment/internal/worker"
)

func queueExec(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("prepare queue fixture: %v", err)
	}
}

func TestRepo_ClaimNext_InvalidOptions(t *testing.T) {
	tooMany := int64(1) << 31
	cases := []struct {
		name  string
		max   int
		lease time.Duration
	}{
		{"zero attempts", 0, time.Second},
		{"negative attempts", -1, time.Second},
		{"attempts outside postgres integer", int(tooMany), time.Second},
		{"zero lease", 3, 0},
		{"negative lease", 3, -time.Second},
		{"lease below database precision", 3, time.Microsecond - 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			_, found, err := NewRepo(nil).ClaimNext(context.Background(), tc.max, tc.lease)
			if err == nil || found {
				t.Fatalf("ClaimNext = found %v, error %v; want validation error", found, err)
			}
		})
	}
}

func TestRepo_ClaimNext_Concurrent(t *testing.T) {
	const jobs, consumers = 64, 8
	pool := newTestPool(t)
	repo := NewRepo(pool)
	queueExec(t, pool, `
		INSERT INTO quote_updates (base, quote, status)
		SELECT 'EUR', 'MXN', 'pending' FROM generate_series(1, $1::integer)`, jobs)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := make(chan struct{})
	claimed := make(chan worker.Job)
	errors := make(chan error, consumers)
	var wg sync.WaitGroup
	for range consumers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				job, found, err := repo.ClaimNext(ctx, 3, time.Hour)
				if err != nil {
					errors <- err
					return
				}
				if !found {
					return
				}
				claimed <- job
			}
		}()
	}
	go func() {
		wg.Wait()
		close(claimed)
		close(errors)
	}()
	close(start)

	seen := make(map[string]bool, jobs)
	for job := range claimed {
		if seen[job.ID] {
			t.Errorf("job %s claimed more than once", job.ID)
		}
		seen[job.ID] = true
		if job.Attempts != 1 || job.Pair != (domain.Pair{Base: "EUR", Quote: "MXN"}) || job.LockedUntil.IsZero() {
			t.Errorf("unexpected claim: %+v", job)
		}
	}
	for err := range errors {
		t.Errorf("concurrent ClaimNext: %v", err)
	}
	if len(seen) != jobs {
		t.Fatalf("claimed %d distinct jobs, want %d", len(seen), jobs)
	}
	var processing int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM quote_updates
		WHERE status = 'processing' AND attempts = 1 AND locked_until > now()`).Scan(&processing); err != nil {
		t.Fatal(err)
	}
	if processing != jobs {
		t.Fatalf("processing rows with first live lease = %d, want %d", processing, jobs)
	}
	if _, found, err := repo.ClaimNext(ctx, 3, time.Hour); err != nil || found {
		t.Fatalf("empty queue: found %v, error %v", found, err)
	}
}

func TestRepo_ClaimNext_SetsLeaseAndClearsPreviousError(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	queueExec(t, pool, `
		INSERT INTO quote_updates (base, quote, status, provider, error, updated_at)
		VALUES ('EUR', 'MXN', 'pending', 'previous', 'temporary failure', now() - interval '1 day')`)
	ctx := context.Background()
	var before, after time.Time
	if err := pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	job, found, err := repo.ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found {
		t.Fatalf("ClaimNext: found %v, error %v", found, err)
	}
	if err := pool.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if job.Attempts != 1 || job.Pair != (domain.Pair{Base: "EUR", Quote: "MXN"}) {
		t.Fatalf("unexpected job: %+v", job)
	}
	if job.LockedUntil.Before(before.Add(time.Hour)) || job.LockedUntil.After(after.Add(time.Hour)) {
		t.Fatalf("lease %v is outside database-time bounds [%v, %v]", job.LockedUntil, before.Add(time.Hour), after.Add(time.Hour))
	}
	var status string
	var attempts int
	var lockedUntil, updatedAt time.Time
	var emptyResult bool
	if err := pool.QueryRow(ctx, `
		SELECT status, attempts, locked_until, updated_at,
		       price IS NULL AND provider IS NULL AND error IS NULL
		FROM quote_updates WHERE id = $1`, job.ID).
		Scan(&status, &attempts, &lockedUntil, &updatedAt, &emptyResult); err != nil {
		t.Fatal(err)
	}
	if status != "processing" || attempts != 1 || !lockedUntil.Equal(job.LockedUntil) || !emptyResult {
		t.Fatalf("stored claim: status %s, attempts %d, lease %v, empty result %v", status, attempts, lockedUntil, emptyResult)
	}
	if updatedAt.Before(before) || updatedAt.After(after) {
		t.Fatalf("updated_at %v is outside database-time bounds [%v, %v]", updatedAt, before, after)
	}
}

func TestRepo_ClaimNext_SkipsUnavailableAndTerminalRows(t *testing.T) {
	pool := newTestPool(t)
	queueExec(t, pool, `
		INSERT INTO quote_updates (base, quote, status, attempts, next_attempt_at, locked_until, price)
		VALUES
		('EUR', 'MXN', 'pending', 0, now() + interval '1 day', NULL, NULL),
		('EUR', 'MXN', 'processing', 3, now(), now() + interval '1 day', NULL),
		('EUR', 'MXN', 'done', 1, now(), NULL, 20.5),
		('EUR', 'MXN', 'failed', 1, now(), NULL, NULL)`)
	if _, found, err := NewRepo(pool).ClaimNext(context.Background(), 3, time.Hour); err != nil || found {
		t.Fatalf("ClaimNext: found %v, error %v; want no ready job", found, err)
	}
	var unchanged int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) FROM quote_updates WHERE
		(status = 'pending' AND attempts = 0) OR
		(status = 'processing' AND attempts = 3 AND locked_until > now()) OR
		(status = 'done' AND attempts = 1 AND price = 20.5) OR
		(status = 'failed' AND attempts = 1)`).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged != 4 {
		t.Fatalf("unchanged unavailable/terminal rows = %d, want 4", unchanged)
	}
}

func TestRepo_ClaimNext_ReclaimsExpiredLease(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	queueExec(t, pool, `
		INSERT INTO quote_updates (base, quote, status, attempts, next_attempt_at, locked_until)
		VALUES ('EUR', 'MXN', 'processing', 1, now() + interval '1 day', now() - interval '1 minute')`)
	first, found, err := repo.ClaimNext(context.Background(), 3, time.Hour)
	if err != nil || !found || first.Attempts != 2 {
		t.Fatalf("first reclaim: job %+v, found %v, error %v", first, found, err)
	}

	queueExec(t, pool, `UPDATE quote_updates SET locked_until = now() - interval '1 minute' WHERE id = $1`, first.ID)
	second, found, err := repo.ClaimNext(context.Background(), 3, time.Hour)
	if err != nil || !found || second.ID != first.ID || second.Attempts != first.Attempts+1 {
		t.Fatalf("second reclaim: job %+v, found %v, error %v", second, found, err)
	}
	if _, found, err := repo.ClaimNext(context.Background(), 3, time.Hour); err != nil || found {
		t.Fatalf("live last-attempt lease: found %v, error %v", found, err)
	}
}

func TestRepo_ClaimNext_ExhaustedOnlyPersistsFailure(t *testing.T) {
	pool := newTestPool(t)
	queueExec(t, pool, `
		INSERT INTO quote_updates (base, quote, status, attempts, next_attempt_at, locked_until)
		VALUES
		('EUR', 'MXN', 'pending', 3, now() + interval '1 day', NULL),
		('EUR', 'MXN', 'processing', 3, now(), now() - interval '1 minute'),
		('EUR', 'MXN', 'processing', 4, now(), now() - interval '1 minute')`)
	if _, found, err := NewRepo(pool).ClaimNext(context.Background(), 3, time.Hour); err != nil || found {
		t.Fatalf("exhausted queue: found %v, error %v", found, err)
	}

	var failed, totalAttempts int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*), COALESCE(sum(attempts), 0) FROM quote_updates
		WHERE status = 'failed' AND locked_until IS NULL`).Scan(&failed, &totalAttempts); err != nil {
		t.Fatal(err)
	}
	if failed != 3 || totalAttempts != 10 {
		t.Fatalf("failed exhausted rows = %d, attempts total = %d; want 3 and 10", failed, totalAttempts)
	}
}

func TestRepo_ClaimNext_ExhaustedDoesNotHideReadyJob(t *testing.T) {
	pool := newTestPool(t)
	queueExec(t, pool, `
		INSERT INTO quote_updates (id, base, quote, status, attempts, next_attempt_at, created_at)
		VALUES
		('00000000-0000-0000-0000-000000000001', 'EUR', 'MXN', 'pending', 3, now() - interval '2 days', now() - interval '2 days'),
		('00000000-0000-0000-0000-000000000002', 'EUR', 'MXN', 'pending', 0, now() - interval '1 day', now() - interval '1 day')`)
	job, found, err := NewRepo(pool).ClaimNext(context.Background(), 3, time.Hour)
	if err != nil || !found || job.ID != "00000000-0000-0000-0000-000000000002" || job.Attempts != 1 {
		t.Fatalf("ClaimNext: job %+v, found %v, error %v", job, found, err)
	}
	var status string
	if err := pool.QueryRow(context.Background(), `
		SELECT status FROM quote_updates WHERE id = '00000000-0000-0000-0000-000000000001'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("exhausted status = %s, want failed", status)
	}
}

func TestRepo_ClaimNext_BoundsExhaustedCleanup(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	queueExec(t, pool, `
		INSERT INTO quote_updates (base, quote, status, attempts)
		SELECT 'EUR', 'MXN', 'pending', 3 FROM generate_series(1, 101)`)
	for call, wantFailed := range []int{100, 101} {
		if _, found, err := repo.ClaimNext(context.Background(), 3, time.Hour); err != nil || found {
			t.Fatalf("cleanup call %d: found %v, error %v", call+1, found, err)
		}
		var failed int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM quote_updates WHERE status = 'failed'`).Scan(&failed); err != nil {
			t.Fatal(err)
		}
		if failed != wantFailed {
			t.Fatalf("cleanup call %d: failed %d, want %d", call+1, failed, wantFailed)
		}
	}
}

func TestRepo_ClaimNext_OrdersByAvailabilityCreationAndID(t *testing.T) {
	pool := newTestPool(t)
	queueExec(t, pool, `
		INSERT INTO quote_updates (id, base, quote, status, attempts, next_attempt_at, locked_until, created_at)
		VALUES
		('00000000-0000-0000-0000-000000000004', 'EUR', 'MXN', 'pending', 0, '2000-01-02 00:00:00+00', NULL, '1999-12-01 00:00:00+00'),
		('00000000-0000-0000-0000-000000000003', 'EUR', 'MXN', 'pending', 0, '2000-01-01 00:00:00+00', NULL, '1999-12-03 00:00:00+00'),
		('00000000-0000-0000-0000-000000000002', 'EUR', 'MXN', 'processing', 1, now() + interval '1 day', '2000-01-01 00:00:00+00', '1999-12-02 00:00:00+00'),
		('00000000-0000-0000-0000-000000000001', 'EUR', 'MXN', 'pending', 0, '2000-01-01 00:00:00+00', NULL, '1999-12-02 00:00:00+00')`)
	repo := NewRepo(pool)
	for i := 1; i <= 4; i++ {
		wantID := fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
		job, found, err := repo.ClaimNext(context.Background(), 3, time.Hour)
		if err != nil || !found || job.ID != wantID {
			t.Fatalf("claim %d: job %+v, found %v, error %v; want %s", i, job, found, err, wantID)
		}
	}
}

func TestRepo_ClaimNext_SkipsLockedCandidate(t *testing.T) {
	pool := newTestPool(t)
	if pool.Config().MaxConns < 2 {
		t.Skip("test requires two database connections")
	}
	queueExec(t, pool, `
		INSERT INTO quote_updates (id, base, quote, status, next_attempt_at)
		VALUES
		('00000000-0000-0000-0000-000000000001', 'EUR', 'MXN', 'pending', now() - interval '2 days'),
		('00000000-0000-0000-0000-000000000002', 'EUR', 'MXN', 'pending', now() - interval '1 day')`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM quote_updates WHERE id = '00000000-0000-0000-0000-000000000001' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	job, found, err := NewRepo(pool).ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found || job.ID != "00000000-0000-0000-0000-000000000002" {
		t.Fatalf("claim while oldest row is locked: job %+v, found %v, error %v", job, found, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	job, found, err = NewRepo(pool).ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found || job.ID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("claim after unlocking oldest row: job %+v, found %v, error %v", job, found, err)
	}
}

func TestRepo_ClaimNext_DatabaseErrorIsNotEmptyQueue(t *testing.T) {
	pool := newTestPool(t)
	pool.Close()
	if _, found, err := NewRepo(pool).ClaimNext(context.Background(), 3, time.Hour); err == nil || found {
		t.Fatalf("closed database: found %v, error %v; want database error", found, err)
	}
}

func TestMigrate_QueueUpgradeAndConstraints(t *testing.T) {
	pool := newTestPoolBeforeMigrations(t)
	ctx := context.Background()
	if err := applyMigration(ctx, pool, "001_init.sql"); err != nil {
		t.Fatal(err)
	}
	queueExec(t, pool, `
		INSERT INTO quote_updates (base, quote, status)
		VALUES ('EUR', 'MXN', 'pending'), ('USD', 'MXN', 'processing')`)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("upgrade existing schema: %v", err)
	}
	var pending, recoverable int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'pending' AND attempts = 0 AND next_attempt_at IS NOT NULL AND locked_until IS NULL),
		       count(*) FILTER (WHERE status = 'processing' AND attempts = 0 AND next_attempt_at IS NOT NULL AND locked_until <= now())
		FROM quote_updates`).Scan(&pending, &recoverable); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || recoverable != 1 {
		t.Fatalf("upgraded legacy rows: pending defaults %d, recoverable processing %d; want 1 each", pending, recoverable)
	}
	var defaults bool
	if err := pool.QueryRow(ctx, `
		INSERT INTO quote_updates (base, quote, status) VALUES ('GBP', 'MXN', 'pending')
		RETURNING attempts = 0 AND next_attempt_at IS NOT NULL AND locked_until IS NULL`).Scan(&defaults); err != nil {
		t.Fatal(err)
	}
	if !defaults {
		t.Fatal("new pending row did not receive queue defaults")
	}
	invalid := []struct{ name, query string }{
		{"negative attempts", `INSERT INTO quote_updates (base, quote, status, attempts) VALUES ('EUR', 'MXN', 'pending', -1)`},
		{"null attempts", `INSERT INTO quote_updates (base, quote, status, attempts) VALUES ('EUR', 'MXN', 'pending', NULL)`},
		{"null retry time", `INSERT INTO quote_updates (base, quote, status, next_attempt_at) VALUES ('EUR', 'MXN', 'pending', NULL)`},
		{"processing without lease", `INSERT INTO quote_updates (base, quote, status) VALUES ('EUR', 'MXN', 'processing')`},
		{"pending with lease", `INSERT INTO quote_updates (base, quote, status, locked_until) VALUES ('EUR', 'MXN', 'pending', now())`},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, tc.query); err == nil {
				t.Fatal("expected database constraint violation")
			}
		})
	}
}
