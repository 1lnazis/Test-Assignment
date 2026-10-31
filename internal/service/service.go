// Package service — бизнес-логика: проверка входных данных и работа с хранилищем.
// Пакет не знает ни про HTTP, ни про конкретную базу данных.
package service

import (
	"context"
	"fmt"

	"github.com/1lnazis/Test-Assignment/internal/domain"
)

// Repository — то, что сервису нужно от хранилища. Интерфейс объявлен здесь,
// на стороне потребителя, поэтому в тестах его легко заменить заглушкой.
type Repository interface {
	CreateUpdate(ctx context.Context, pair domain.Pair) (domain.Update, error)
	GetUpdate(ctx context.Context, id string) (domain.Update, error)
	LatestQuote(ctx context.Context, pair domain.Pair) (domain.Quote, error)
}

// Service — операции над котировками.
type Service struct {
	repo       Repository
	currencies domain.Currencies
}

// New создаёт сервис. currencies — набор валют, которые он принимает.
func New(repo Repository, currencies domain.Currencies) *Service {
	return &Service{repo: repo, currencies: currencies}
}

// RequestUpdate проверяет пару и создаёт запрос на обновление котировки.
// Сам запрос выполняется позже, в фоне, поэтому здесь только постановка в очередь.
func (s *Service) RequestUpdate(ctx context.Context, rawPair string) (domain.Update, error) {
	pair, err := domain.ParsePair(rawPair, s.currencies)
	if err != nil {
		return domain.Update{}, err
	}
	return s.repo.CreateUpdate(ctx, pair)
}

// GetUpdate возвращает запрос на обновление по идентификатору.
func (s *Service) GetUpdate(ctx context.Context, id string) (domain.Update, error) {
	if !isUUID(id) {
		return domain.Update{}, fmt.Errorf("%w: %q", domain.ErrInvalidID, id)
	}
	return s.repo.GetUpdate(ctx, id)
}

// LatestQuote возвращает последнюю котировку по паре.
func (s *Service) LatestQuote(ctx context.Context, rawPair string) (domain.Quote, error) {
	pair, err := domain.ParsePair(rawPair, s.currencies)
	if err != nil {
		return domain.Quote{}, err
	}
	return s.repo.LatestQuote(ctx, pair)
}

// isUUID проверяет формат xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx.
// Без этой проверки кривой id дошёл бы до базы и вернул бы оттуда ошибку формата.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
