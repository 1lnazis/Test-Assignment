// Package worker описывает обработку фоновых джоб котировок.
package worker

import (
	"context"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/domain"
	"github.com/shopspring/decimal"
)

// Job — данные, которые воркер получает после захвата джобы.
// Attempts — номер выданной попытки и токен для защиты последующей записи.
type Job struct {
	ID          string
	Pair        domain.Pair
	Attempts    int
	LockedUntil time.Time
}

// Queue объявляется потребителем — пакетом воркера.
// found=false и err=nil означают отсутствие доступной работы.
type Queue interface {
	ClaimNext(ctx context.Context, maxAttempts int, lease time.Duration) (job Job, found bool, err error)
}

// Quoter — операции, которые воркеру нужны от клиента провайдера.
type Quoter interface {
	Fetch(ctx context.Context, pair domain.Pair) (decimal.Decimal, error)
	Name() string
}
