package domain

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrNotFound = errors.New("not found")

	ErrInvalidID = errors.New("invalid id")
)

type Update struct {
	ID     string
	Pair   Pair
	Status Status

	Price *decimal.Decimal

	Provider string

	Error     string
	CreatedAt time.Time

	UpdatedAt time.Time
}

type Quote struct {
	Pair      Pair
	Price     decimal.Decimal
	Provider  string
	UpdatedAt time.Time
}
