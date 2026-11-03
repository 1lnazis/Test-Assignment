package worker

import (
	"context"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/domain"
	"github.com/shopspring/decimal"
)

type Job struct {
	ID          string
	Pair        domain.Pair
	Attempts    int
	LockedUntil time.Time
}

type Queue interface {
	ClaimNext(ctx context.Context, maxAttempts int, lease time.Duration) (job Job, found bool, err error)
	Complete(ctx context.Context, job Job, price decimal.Decimal, provider string) (saved bool, err error)
	Retry(ctx context.Context, job Job, reason string, delay time.Duration) (saved bool, err error)
	Fail(ctx context.Context, job Job, reason string) (saved bool, err error)
}

type Quoter interface {
	Fetch(ctx context.Context, pair domain.Pair) (decimal.Decimal, error)
	Name() string
}
