package domain

import (
	"errors"
	"fmt"
	"strings"
)

type Currency string

type Pair struct {
	Base  Currency
	Quote Currency
}

func (p Pair) String() string {
	return string(p.Base) + "/" + string(p.Quote)
}

var (
	ErrInvalidCurrency     = errors.New("invalid currency code")
	ErrInvalidPair         = errors.New("invalid pair, expected BASE/QUOTE")
	ErrSameCurrency        = errors.New("base and quote must differ")
	ErrUnsupportedCurrency = errors.New("unsupported currency")
)

type Currencies map[Currency]struct{}

func NewCurrencies(codes ...Currency) Currencies {
	set := make(Currencies, len(codes))
	for _, c := range codes {
		set[c] = struct{}{}
	}
	return set
}

func (c Currencies) Has(cur Currency) bool {
	_, ok := c[cur]
	return ok
}

func ParseCurrency(s string) (Currency, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 3 {
		return "", fmt.Errorf("%w: %q", ErrInvalidCurrency, s)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return "", fmt.Errorf("%w: %q", ErrInvalidCurrency, s)
		}
	}
	return Currency(s), nil
}

func ParsePair(s string, allowed Currencies) (Pair, error) {
	rawBase, rawQuote, ok := strings.Cut(s, "/")
	if !ok {
		return Pair{}, fmt.Errorf("%w: %q", ErrInvalidPair, s)
	}

	base, errBase := ParseCurrency(rawBase)
	quote, errQuote := ParseCurrency(rawQuote)
	if errBase != nil || errQuote != nil {
		return Pair{}, fmt.Errorf("%w: %q", ErrInvalidPair, s)
	}

	if base == quote {
		return Pair{}, fmt.Errorf("%w: %q", ErrSameCurrency, s)
	}
	for _, c := range [...]Currency{base, quote} {
		if !allowed.Has(c) {
			return Pair{}, fmt.Errorf("%w: %s", ErrUnsupportedCurrency, c)
		}
	}

	return Pair{Base: base, Quote: quote}, nil
}
