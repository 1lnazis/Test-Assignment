package postgres

import (
	"context"
	"fmt"

	"github.com/1lnazis/Test-Assignment/internal/worker"
	"github.com/shopspring/decimal"
)

func (r *Repo) Complete(ctx context.Context, job worker.Job, price decimal.Decimal, provider string) (saved bool, err error) {
	if job.Attempts <= 0 {
		return false, fmt.Errorf("complete job: attempt must be positive")
	}

	price = price.Round(10)
	if !price.IsPositive() || price.GreaterThanOrEqual(decimal.New(1, 14)) {
		return false, fmt.Errorf("complete job: rounded price must be positive and less than 100000000000000")
	}

	const q = `
		UPDATE quote_updates
		SET status = 'done',
			price = $3::numeric,
			provider = NULLIF($4::text, ''),
			error = NULL,
			locked_until = NULL,
			updated_at = now()
		WHERE id = $1
		  AND status = 'processing'
		  AND attempts = $2`

	tag, err := r.pool.Exec(ctx, q, job.ID, job.Attempts, price.String(), provider)
	if err != nil {
		return false, fmt.Errorf("complete job: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
