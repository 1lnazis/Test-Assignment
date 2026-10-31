package domain

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// Ошибки, общие для всех слоёв сервиса.
var (
	// ErrNotFound — запрошенной записи нет.
	ErrNotFound = errors.New("not found")
	// ErrInvalidID — идентификатор запроса имеет неверный формат.
	ErrInvalidID = errors.New("invalid id")
)

// Update — запрос на обновление котировки и его результат.
type Update struct {
	ID     string
	Pair   Pair
	Status Status
	// Price заполняется, когда запрос выполнен (StatusDone), иначе nil.
	Price *decimal.Decimal
	// Provider — какой провайдер дал цену.
	Provider string
	// Error — причина неудачи (StatusFailed).
	Error     string
	CreatedAt time.Time
	// UpdatedAt — время последнего изменения записи. Для выполненного запроса
	// это момент, когда цена сохранена.
	UpdatedAt time.Time
}

// Quote — котировка: цена пары на момент UpdatedAt.
type Quote struct {
	Pair      Pair
	Price     decimal.Decimal
	Provider  string
	UpdatedAt time.Time
}
