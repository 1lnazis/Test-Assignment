package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/worker"
)

func (r *Repo) Retry(ctx context.Context, job worker.Job, reason string, delay time.Duration) (bool, error) {
	if err := validateFailure(job, reason); err != nil {
		return false, err
	}
	if delay < 0 || (delay > 0 && delay < time.Microsecond) {
		return false, fmt.Errorf("retry job: delay must be zero or at least one microsecond")
	}
	const q = `
		UPDATE quote_updates
		SET status = 'pending',
			error = $3,
			next_attempt_at = now() + $4::bigint * interval '1 microsecond',
			locked_until = NULL,
			price = NULL,
			provider = NULL,
			updated_at = now()
		WHERE id = $1 AND status = 'processing' AND attempts = $2`
	tag, err := r.pool.Exec(ctx, q, job.ID, job.Attempts, reason, delay.Microseconds())
	if err != nil {
		return false, fmt.Errorf("retry job: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repo) Fail(ctx context.Context, job worker.Job, reason string) (bool, error) {
	if err := validateFailure(job, reason); err != nil {
		return false, err
	}
	const q = `
		UPDATE quote_updates
		SET status = 'failed',
			error = $3,
			locked_until = NULL,
			price = NULL,
			provider = NULL,
			updated_at = now()
		WHERE id = $1 AND status = 'processing' AND attempts = $2`
	tag, err := r.pool.Exec(ctx, q, job.ID, job.Attempts, reason)
	if err != nil {
		return false, fmt.Errorf("fail job: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func validateFailure(job worker.Job, reason string) error {
	if job.Attempts <= 0 {
		return fmt.Errorf("save failure: attempt must be positive")
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("save failure: reason must not be empty")
	}
	return nil
}
