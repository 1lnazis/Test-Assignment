package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/1lnazis/Test-Assignment/internal/domain"
)

type Repository interface {
	CreateUpdate(ctx context.Context, pair domain.Pair) (domain.Update, error)
	GetUpdate(ctx context.Context, id string) (domain.Update, error)
	LatestQuote(ctx context.Context, pair domain.Pair) (domain.Quote, error)
}

type Service struct {
	repo       Repository
	currencies domain.Currencies
}

func New(repo Repository, currencies domain.Currencies) *Service {
	return &Service{repo: repo, currencies: currencies}
}

func (s *Service) RequestUpdate(ctx context.Context, rawPair string) (domain.Update, error) {
	pair, err := domain.ParsePair(rawPair, s.currencies)
	if err != nil {
		return domain.Update{}, err
	}
	return s.repo.CreateUpdate(ctx, pair)
}

func (s *Service) GetUpdate(ctx context.Context, id string) (domain.Update, error) {
	if !isUUID(id) {
		return domain.Update{}, fmt.Errorf("%w: %q", domain.ErrInvalidID, id)
	}
	return s.repo.GetUpdate(ctx, id)
}

func (s *Service) LatestQuote(ctx context.Context, rawPair string) (domain.Quote, error) {
	pair, err := domain.ParsePair(rawPair, s.currencies)
	if err != nil {
		return domain.Quote{}, err
	}
	return s.repo.LatestQuote(ctx, pair)
}

func isUUID(s string) bool {
	return len(s) == 36 && uuid.Validate(s) == nil
}
