package postgres

import (
	"context"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const migrationLockID = 727001

func NewPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	for _, e := range entries {
		if err := applyMigration(ctx, pool, e.Name()); err != nil {
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, pool *pgxpool.Pool, name string) error {
	script, err := migrationsFS.ReadFile("migrations/" + name)
	if err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text        PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}

	var applied bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
	).Scan(&applied); err != nil {
		return err
	}
	if applied {
		return tx.Commit(ctx)
	}

	if _, err := tx.Exec(ctx, string(script)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
