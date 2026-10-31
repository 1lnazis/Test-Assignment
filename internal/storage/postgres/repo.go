package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/1lnazis/Test-Assignment/internal/domain"
)

// Repo — репозиторий запросов на обновление и котировок.
type Repo struct {
	pool *pgxpool.Pool
}

// NewRepo создаёт репозиторий поверх пула соединений.
func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// CreateUpdate сохраняет новый запрос на обновление пары со статусом pending.
func (r *Repo) CreateUpdate(ctx context.Context, pair domain.Pair) (domain.Update, error) {
	const q = `
		INSERT INTO quote_updates (base, quote, status)
		VALUES ($1, $2, $3)
		RETURNING id::text, created_at, updated_at`

	u := domain.Update{Pair: pair, Status: domain.StatusPending}
	err := r.pool.QueryRow(ctx, q,
		string(pair.Base), string(pair.Quote), string(domain.StatusPending),
	).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return domain.Update{}, fmt.Errorf("insert update: %w", err)
	}
	return u, nil
}

// GetUpdate возвращает запрос по идентификатору или domain.ErrNotFound.
func (r *Repo) GetUpdate(ctx context.Context, id string) (domain.Update, error) {
	const q = `
		SELECT id::text, base, quote, status, price::text, provider, error, created_at, updated_at
		FROM quote_updates
		WHERE id = $1`

	var (
		u                         domain.Update
		base, quote, status       string
		price, provider, errorMsg *string
	)
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&u.ID, &base, &quote, &status, &price, &provider, &errorMsg, &u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Update{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Update{}, fmt.Errorf("select update: %w", err)
	}

	u.Pair = domain.Pair{Base: domain.Currency(base), Quote: domain.Currency(quote)}
	u.Status = domain.Status(status)
	if price != nil {
		p, err := decimal.NewFromString(*price)
		if err != nil {
			return domain.Update{}, fmt.Errorf("parse price %q: %w", *price, err)
		}
		u.Price = &p
	}
	if provider != nil {
		u.Provider = *provider
	}
	if errorMsg != nil {
		u.Error = *errorMsg
	}
	return u, nil
}

// LatestQuote возвращает последнюю котировку по паре среди выполненных запросов
// или domain.ErrNotFound, если выполненных запросов ещё не было.
func (r *Repo) LatestQuote(ctx context.Context, pair domain.Pair) (domain.Quote, error) {
	const q = `
		SELECT price::text, COALESCE(provider, ''), updated_at
		FROM quote_updates
		WHERE base = $1 AND quote = $2 AND status = 'done'
		ORDER BY updated_at DESC
		LIMIT 1`

	quote := domain.Quote{Pair: pair}
	var price string
	err := r.pool.QueryRow(ctx, q, string(pair.Base), string(pair.Quote)).
		Scan(&price, &quote.Provider, &quote.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Quote{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Quote{}, fmt.Errorf("select latest quote: %w", err)
	}

	quote.Price, err = decimal.NewFromString(price)
	if err != nil {
		return domain.Quote{}, fmt.Errorf("parse price %q: %w", price, err)
	}
	return quote, nil
}
