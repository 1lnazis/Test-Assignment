package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/domain"
	"github.com/1lnazis/Test-Assignment/internal/worker"
	"github.com/shopspring/decimal"
)

func TestRepo_Complete_InvalidInput(t *testing.T) {
	cases := []struct {
		name     string
		attempts int
		price    string
	}{
		{"zero attempt", 0, "21.072"},
		{"negative attempt", -1, "21.072"},
		{"zero price", 1, "0"},
		{"negative price", 1, "-1"},
		{"rounds to zero", 1, "0.00000000001"},
		{"above numeric range", 1, "100000000000000"},
		{"rounds out of numeric range", 1, "99999999999999.99999999995"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			price, err := decimal.NewFromString(tc.price)
			if err != nil {
				t.Fatal(err)
			}

			saved, err := NewRepo(nil).Complete(context.Background(), worker.Job{Attempts: tc.attempts}, price, "test")
			if err == nil || saved {
				t.Fatalf("Complete = saved %v, error %v; want validation error", saved, err)
			}
		})
	}
}

func TestRepo_Complete_SavesRoundedPriceAndReleasesLease(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	ctx := context.Background()
	created, err := repo.CreateUpdate(ctx, domain.Pair{Base: "EUR", Quote: "MXN"})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := repo.ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found || job.ID != created.ID {
		t.Fatalf("claim: job %+v, found %v, error %v", job, found, err)
	}
	queueExec(t, pool, `UPDATE quote_updates SET error = 'previous error', provider = 'old', updated_at = now() - interval '1 day' WHERE id = $1`, job.ID)
	var before, after time.Time
	if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	price, err := decimal.NewFromString("21.07212345675")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := repo.Complete(ctx, job, price, "frankfurter")
	if err != nil || !saved {
		t.Fatalf("Complete: saved %v, error %v", saved, err)
	}
	if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetUpdate(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusDone || got.Price == nil || got.Price.String() != "21.0721234568" || got.Provider != "frankfurter" || got.Error != "" {
		t.Fatalf("saved update: %+v", got)
	}
	if got.UpdatedAt.Before(before) || got.UpdatedAt.After(after) {
		t.Fatalf("updated_at %v outside database time range [%v, %v]", got.UpdatedAt, before, after)
	}
	var released bool
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT locked_until IS NULL, attempts FROM quote_updates WHERE id = $1`, job.ID).Scan(&released, &attempts); err != nil {
		t.Fatal(err)
	}
	if !released || attempts != job.Attempts {
		t.Fatalf("released %v, attempts %d; want true and %d", released, attempts, job.Attempts)
	}
	latest, err := repo.LatestQuote(ctx, job.Pair)
	if err != nil || latest.Price.String() != "21.0721234568" || latest.Provider != "frankfurter" {
		t.Fatalf("latest quote: %+v, error %v", latest, err)
	}
}

func TestRepo_Complete_StaleAttemptCannotOverwriteNewAttempt(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	ctx := context.Background()
	if _, err := repo.CreateUpdate(ctx, domain.Pair{Base: "EUR", Quote: "MXN"}); err != nil {
		t.Fatal(err)
	}
	oldJob, found, err := repo.ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found {
		t.Fatalf("first claim: found %v, error %v", found, err)
	}
	queueExec(t, pool, `UPDATE quote_updates SET locked_until = now() - interval '1 minute' WHERE id = $1`, oldJob.ID)
	newJob, found, err := repo.ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found || newJob.ID != oldJob.ID || newJob.Attempts != oldJob.Attempts+1 {
		t.Fatalf("reclaim: job %+v, found %v, error %v", newJob, found, err)
	}
	if saved, err := repo.Complete(ctx, oldJob, decimal.NewFromInt(10), "old"); err != nil || saved {
		t.Fatalf("stale completion: saved %v, error %v", saved, err)
	}
	got, err := repo.GetUpdate(ctx, newJob.ID)
	if err != nil || got.Status != domain.StatusProcessing || got.Price != nil {
		t.Fatalf("new attempt after stale write: %+v, error %v", got, err)
	}
	if saved, err := repo.Complete(ctx, newJob, decimal.NewFromInt(21), "new"); err != nil || !saved {
		t.Fatalf("current completion: saved %v, error %v", saved, err)
	}
	if saved, err := repo.Complete(ctx, oldJob, decimal.NewFromInt(10), "old"); err != nil || saved {
		t.Fatalf("stale write after done: saved %v, error %v", saved, err)
	}
	got, err = repo.GetUpdate(ctx, newJob.ID)
	if err != nil || got.Price == nil || got.Price.String() != "21" || got.Provider != "new" {
		t.Fatalf("result was overwritten: %+v, error %v", got, err)
	}
}

func TestRepo_Complete_ExpiredLeaseWithoutTakeover(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	ctx := context.Background()
	if _, err := repo.CreateUpdate(ctx, domain.Pair{Base: "EUR", Quote: "MXN"}); err != nil {
		t.Fatal(err)
	}
	job, found, err := repo.ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found {
		t.Fatalf("claim: found %v, error %v", found, err)
	}
	queueExec(t, pool, `UPDATE quote_updates SET locked_until = now() - interval '1 minute' WHERE id = $1`, job.ID)

	if saved, err := repo.Complete(ctx, job, decimal.NewFromInt(21), "test"); err != nil || !saved {
		t.Fatalf("late result without takeover: saved %v, error %v", saved, err)
	}
}

func TestRepo_Complete_RejectsNonProcessingStates(t *testing.T) {
	for _, status := range []string{"pending", "failed", "done"} {
		t.Run(status, func(t *testing.T) {
			pool := newTestPool(t)
			var id string
			if err := pool.QueryRow(context.Background(), `
				INSERT INTO quote_updates (base, quote, status, attempts, price)
				VALUES ('EUR', 'MXN', $1, 1, CASE WHEN $1 = 'done' THEN 20::numeric ELSE NULL END)
				RETURNING id::text`, status).Scan(&id); err != nil {
				t.Fatal(err)
			}
			repo := NewRepo(pool)
			saved, err := repo.Complete(context.Background(), worker.Job{ID: id, Attempts: 1}, decimal.NewFromInt(99), "unexpected")
			if err != nil || saved {
				t.Fatalf("Complete on %s: saved %v, error %v", status, saved, err)
			}
			got, err := repo.GetUpdate(context.Background(), id)
			if err != nil || string(got.Status) != status || got.Provider != "" {
				t.Fatalf("state changed: %+v, error %v", got, err)
			}
			if status == "done" && (got.Price == nil || got.Price.String() != "20") {
				t.Fatalf("done price was overwritten: %+v", got)
			}
		})
	}
}

func TestRepo_Complete_PriceRangeAndMissingJob(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	ctx := context.Background()
	if _, err := repo.CreateUpdate(ctx, domain.Pair{Base: "EUR", Quote: "MXN"}); err != nil {
		t.Fatal(err)
	}
	job, found, err := repo.ClaimNext(ctx, 3, time.Hour)
	if err != nil || !found {
		t.Fatalf("claim: found %v, error %v", found, err)
	}
	price, err := decimal.NewFromString("99999999999999.9999999999")
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := repo.Complete(ctx, job, price, ""); err != nil || !saved {
		t.Fatalf("largest numeric price: saved %v, error %v", saved, err)
	}
	got, err := repo.GetUpdate(ctx, job.ID)
	if err != nil || got.Price == nil || !got.Price.Equal(price) || got.Provider != "" {
		t.Fatalf("stored boundary price: %+v, error %v", got, err)
	}
	missing := worker.Job{ID: "00000000-0000-4000-8000-000000000001", Attempts: 1}
	if saved, err := repo.Complete(ctx, missing, decimal.NewFromInt(1), "test"); err != nil || saved {
		t.Fatalf("missing job: saved %v, error %v", saved, err)
	}
}

func TestRepo_Complete_CancellationAndDatabaseFailure(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	job := worker.Job{ID: "00000000-0000-4000-8000-000000000001", Attempts: 1}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if saved, err := repo.Complete(ctx, job, decimal.NewFromInt(1), "test"); saved || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: saved %v, error %v", saved, err)
	}
	pool.Close()
	if saved, err := repo.Complete(context.Background(), job, decimal.NewFromInt(1), "test"); saved || err == nil {
		t.Fatalf("closed database: saved %v, error %v", saved, err)
	}
}
