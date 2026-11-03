package service

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/1lnazis/Test-Assignment/internal/domain"
)

type fakeRepo struct {
	created []domain.Pair
	updates map[string]domain.Update
	quotes  map[domain.Pair]domain.Quote
	err     error
}

func (f *fakeRepo) CreateUpdate(_ context.Context, pair domain.Pair) (domain.Update, error) {
	if f.err != nil {
		return domain.Update{}, f.err
	}
	f.created = append(f.created, pair)
	return domain.Update{ID: "11111111-1111-4111-8111-111111111111", Pair: pair, Status: domain.StatusPending}, nil
}

func (f *fakeRepo) GetUpdate(_ context.Context, id string) (domain.Update, error) {
	if f.err != nil {
		return domain.Update{}, f.err
	}
	u, ok := f.updates[id]
	if !ok {
		return domain.Update{}, domain.ErrNotFound
	}
	return u, nil
}

func (f *fakeRepo) LatestQuote(_ context.Context, pair domain.Pair) (domain.Quote, error) {
	if f.err != nil {
		return domain.Quote{}, f.err
	}
	q, ok := f.quotes[pair]
	if !ok {
		return domain.Quote{}, domain.ErrNotFound
	}
	return q, nil
}

func newService(repo *fakeRepo) *Service {
	return New(repo, domain.NewCurrencies("USD", "EUR", "MXN"))
}

func TestService_RequestUpdate(t *testing.T) {
	repo := &fakeRepo{}
	svc := newService(repo)

	u, err := svc.RequestUpdate(context.Background(), " eur/mxn ")
	if err != nil {
		t.Fatalf("RequestUpdate: %v", err)
	}
	if u.Pair != (domain.Pair{Base: "EUR", Quote: "MXN"}) || u.Status != domain.StatusPending {
		t.Fatalf("unexpected update: %+v", u)
	}
	if len(repo.created) != 1 {
		t.Fatalf("repo.CreateUpdate calls = %d, want 1", len(repo.created))
	}
}

func TestService_RequestUpdate_InvalidPairDoesNotTouchRepo(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr error
	}{
		{"bad format", "EURMXN", domain.ErrInvalidPair},
		{"same currency", "USD/USD", domain.ErrSameCurrency},
		{"unsupported", "JPY/USD", domain.ErrUnsupportedCurrency},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{}
			_, err := newService(repo).RequestUpdate(context.Background(), tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if len(repo.created) != 0 {
				t.Fatalf("repo must not be called for invalid input")
			}
		})
	}
}

func TestService_RequestUpdate_RepoError(t *testing.T) {
	boom := errors.New("db is down")
	_, err := newService(&fakeRepo{err: boom}).RequestUpdate(context.Background(), "EUR/MXN")
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
}

func TestService_GetUpdate(t *testing.T) {
	const id = "22222222-2222-4222-8222-222222222222"
	repo := &fakeRepo{updates: map[string]domain.Update{
		id: {ID: id, Status: domain.StatusPending},
	}}
	svc := newService(repo)

	got, err := svc.GetUpdate(context.Background(), id)
	if err != nil || got.ID != id {
		t.Fatalf("GetUpdate = %+v, %v", got, err)
	}

	_, err = svc.GetUpdate(context.Background(), "33333333-3333-4333-8333-333333333333")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrNotFound)
	}

	_, err = svc.GetUpdate(context.Background(), "not-a-uuid")
	if !errors.Is(err, domain.ErrInvalidID) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidID)
	}
}

func TestService_LatestQuote(t *testing.T) {
	pair := domain.Pair{Base: "EUR", Quote: "MXN"}
	repo := &fakeRepo{quotes: map[domain.Pair]domain.Quote{
		pair: {Pair: pair, Price: decimal.RequireFromString("21.5")},
	}}
	svc := newService(repo)

	got, err := svc.LatestQuote(context.Background(), "EUR/MXN")
	if err != nil || got.Price.String() != "21.5" {
		t.Fatalf("LatestQuote = %+v, %v", got, err)
	}

	_, err = svc.LatestQuote(context.Background(), "USD/EUR")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrNotFound)
	}

	_, err = svc.LatestQuote(context.Background(), "garbage")
	if !errors.Is(err, domain.ErrInvalidPair) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidPair)
	}
}

func TestIsUUID(t *testing.T) {
	tests := map[string]bool{
		"11111111-1111-4111-8111-111111111111":  true,
		"AAAAAAAA-bbbb-4ccc-8ddd-eeeeeeeeeeee":  true,
		"":                                      false,
		"not-a-uuid":                            false,
		"11111111111141118111111111111111":      false,
		"11111111-1111-4111-8111-11111111111g":  false,
		"11111111-1111-4111-8111-1111111111111": false,
	}
	for in, want := range tests {
		if got := isUUID(in); got != want {
			t.Errorf("isUUID(%q) = %v, want %v", in, got, want)
		}
	}
}
