package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/1lnazis/Test-Assignment/internal/domain"
	"github.com/1lnazis/Test-Assignment/internal/worker"
)

// Не обновляем неограниченное число исчерпавших попытки строк в одном захвате.
const reapBatchSize = 100

var _ worker.Queue = (*Repo)(nil)

// ClaimNext выдаёт одну готовую джобу и сохраняет начало её попытки.
// Pending готова по next_attempt_at; processing — после окончания аренды.
// В том же SQL до 100 исчерпавших попытки джоб переводятся в failed.
// Метод использует один autocommit-запрос; HTTP-вызов выполняется уже после него.
func (r *Repo) ClaimNext(ctx context.Context, maxAttempts int, lease time.Duration) (worker.Job, bool, error) {
	// attempts хранится в PostgreSQL integer. Нулевая после округления аренда
	// позволила бы следующему воркеру немедленно перезахватить ту же запись.
	if maxAttempts <= 0 || int64(maxAttempts) > 1<<31-1 {
		return worker.Job{}, false, fmt.Errorf("claim next: max attempts must fit a positive PostgreSQL integer")
	}
	if lease < time.Microsecond {
		return worker.Job{}, false, fmt.Errorf("claim next: lease must be at least one microsecond")
	}

	const q = `
		WITH exhausted AS (
			SELECT id
			FROM quote_updates
			WHERE attempts >= $1
			  AND (
				status = 'pending'
				OR (status = 'processing' AND locked_until <= now())
			  )
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT $3
		), reaped AS (
			UPDATE quote_updates q
			SET status = 'failed',
				locked_until = NULL,
				price = NULL,
				provider = NULL,
				error = COALESCE(NULLIF(q.error, ''), 'maximum attempts reached'),
				updated_at = now()
			FROM exhausted e
			WHERE q.id = e.id
			RETURNING q.id
		), next AS (
			SELECT id
			FROM quote_updates
			WHERE status IN ('pending', 'processing')
			  AND attempts < $1
			  AND CASE WHEN status = 'pending'
					   THEN next_attempt_at ELSE locked_until END <= now()
			ORDER BY CASE WHEN status = 'pending'
						  THEN next_attempt_at ELSE locked_until END,
					 created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE quote_updates q
		SET status = 'processing',
			attempts = q.attempts + 1,
			locked_until = now() + $2::bigint * interval '1 microsecond',
			price = NULL,
			provider = NULL,
			error = NULL,
			updated_at = now()
		FROM next
		WHERE q.id = next.id
		RETURNING q.id::text, q.base, q.quote, q.attempts, q.locked_until`

	var job worker.Job
	var base, quote string
	err := r.pool.QueryRow(ctx, q, maxAttempts, lease.Microseconds(), reapBatchSize).
		Scan(&job.ID, &base, &quote, &job.Attempts, &job.LockedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		// В autocommit reaped сохраняется и при отсутствии выдаваемой джобы.
		return worker.Job{}, false, nil
	}
	if err != nil {
		return worker.Job{}, false, fmt.Errorf("claim next: %w", err)
	}
	job.Pair = domain.Pair{Base: domain.Currency(base), Quote: domain.Currency(quote)}
	return job, true, nil
}
