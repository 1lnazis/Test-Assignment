package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/1lnazis/Test-Assignment/internal/domain"
)

// newTestPool возвращает пул, привязанный к отдельной схеме, которая создаётся
// под тест и удаляется после него, поэтому тесты не мешают друг другу и данным в базе.
// Без TEST_DATABASE_URL интеграционные тесты пропускаются.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL не задан, интеграционный тест пропущен")
	}
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schema := "test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

func TestMigrate_Idempotent(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()

	// newTestPool уже применил миграции, повторный запуск ничего не должен ломать.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("schema_migrations rows = %d, want 1", n)
	}
}

func TestRepo_CreateAndGetUpdate(t *testing.T) {
	repo := NewRepo(newTestPool(t))
	ctx := context.Background()
	pair := domain.Pair{Base: "EUR", Quote: "MXN"}

	created, err := repo.CreateUpdate(ctx, pair)
	if err != nil {
		t.Fatalf("CreateUpdate: %v", err)
	}
	if created.ID == "" || created.Status != domain.StatusPending || created.Pair != pair {
		t.Fatalf("unexpected created update: %+v", created)
	}

	got, err := repo.GetUpdate(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUpdate: %v", err)
	}
	if got.ID != created.ID || got.Pair != pair || got.Status != domain.StatusPending {
		t.Fatalf("GetUpdate = %+v, want pending EUR/MXN with id %s", got, created.ID)
	}
	if got.Price != nil || got.Provider != "" || got.Error != "" {
		t.Fatalf("pending update must have no result, got %+v", got)
	}
}

func TestRepo_GetUpdate_NotFound(t *testing.T) {
	repo := NewRepo(newTestPool(t))

	_, err := repo.GetUpdate(context.Background(), "11111111-1111-4111-8111-111111111111")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrNotFound)
	}
}

func TestRepo_LatestQuote(t *testing.T) {
	pool := newTestPool(t)
	repo := NewRepo(pool)
	ctx := context.Background()
	pair := domain.Pair{Base: "EUR", Quote: "MXN"}

	// Пока выполненных запросов нет, а pending-запрос не считается.
	if _, err := repo.CreateUpdate(ctx, pair); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LatestQuote(ctx, pair); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrNotFound)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	insertDone := func(base, quote, price, provider string, at time.Time) {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO quote_updates (base, quote, status, price, provider, updated_at)
			VALUES ($1, $2, 'done', $3::numeric, $4, $5)`,
			base, quote, price, provider, at)
		if err != nil {
			t.Fatalf("insert done row: %v", err)
		}
	}
	insertDone("EUR", "MXN", "20.5", "old", now.Add(-time.Hour))
	insertDone("EUR", "MXN", "21.25", "fresh", now)
	insertDone("USD", "MXN", "17", "other-pair", now.Add(time.Minute))

	got, err := repo.LatestQuote(ctx, pair)
	if err != nil {
		t.Fatalf("LatestQuote: %v", err)
	}
	if got.Price.String() != "21.25" || got.Provider != "fresh" {
		t.Fatalf("LatestQuote = %+v, want price 21.25 from provider fresh", got)
	}
	if !got.UpdatedAt.Equal(now) {
		t.Fatalf("UpdatedAt = %v, want %v", got.UpdatedAt, now)
	}
}

func TestRepo_DoneRowRequiresPrice(t *testing.T) {
	pool := newTestPool(t)

	// Ограничение в таблице не даёт сохранить выполненный запрос без цены.
	_, err := pool.Exec(context.Background(),
		`INSERT INTO quote_updates (base, quote, status) VALUES ('EUR', 'MXN', 'done')`)
	if err == nil {
		t.Fatal("expected constraint violation, got nil")
	}
}
