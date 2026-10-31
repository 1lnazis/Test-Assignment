package domain

import (
	"errors"
	"testing"
)

func TestParsePair(t *testing.T) {
	allowed := NewCurrencies("USD", "EUR", "MXN")

	tests := []struct {
		name    string
		in      string
		want    Pair
		wantErr error
	}{
		{"ok", "EUR/MXN", Pair{"EUR", "MXN"}, nil},
		{"lowercase and spaces", " eur / mxn ", Pair{"EUR", "MXN"}, nil},
		{"empty", "", Pair{}, ErrInvalidPair},
		{"no slash", "EURMXN", Pair{}, ErrInvalidPair},
		{"too many parts", "EUR/MXN/USD", Pair{}, ErrInvalidPair},
		{"short code", "EU/MXN", Pair{}, ErrInvalidPair},
		{"digits in code", "EU1/MXN", Pair{}, ErrInvalidPair},
		{"empty quote", "EUR/", Pair{}, ErrInvalidPair},
		{"same currency", "USD/USD", Pair{}, ErrSameCurrency},
		{"unsupported base", "JPY/USD", Pair{}, ErrUnsupportedCurrency},
		{"unsupported quote", "USD/JPY", Pair{}, ErrUnsupportedCurrency},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePair(tc.in, allowed)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ParsePair(%q) error = %v, want %v", tc.in, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("ParsePair(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParsePair_NilAllowedSet(t *testing.T) {
	_, err := ParsePair("USD/EUR", nil)
	if !errors.Is(err, ErrUnsupportedCurrency) {
		t.Fatalf("error = %v, want %v", err, ErrUnsupportedCurrency)
	}
}

func TestPair_String_RoundTrip(t *testing.T) {
	allowed := NewCurrencies("USD", "EUR")
	p := Pair{Base: "EUR", Quote: "USD"}

	got, err := ParsePair(p.String(), allowed)
	if err != nil {
		t.Fatalf("ParsePair(%q): %v", p.String(), err)
	}
	if got != p {
		t.Fatalf("round trip = %v, want %v", got, p)
	}
}

func TestStatus_Terminal(t *testing.T) {
	tests := map[Status]bool{
		StatusPending:    false,
		StatusProcessing: false,
		StatusDone:       true,
		StatusFailed:     true,
	}
	for s, want := range tests {
		if got := s.Terminal(); got != want {
			t.Errorf("%s.Terminal() = %v, want %v", s, got, want)
		}
	}
}
