// Package domain — основные типы сервиса котировок.
// Здесь ничего нет про HTTP, базу данных и внешних провайдеров.
package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Currency — трёхбуквенный код валюты по ISO 4217 в верхнем регистре, например "USD".
type Currency string

// Pair — упорядоченная валютная пара. Цена пары показывает, сколько единиц Quote
// стоит одна единица Base: EUR/MXN — это цена 1 EUR в MXN.
type Pair struct {
	Base  Currency
	Quote Currency
}

// String возвращает пару в виде BASE/QUOTE, ровно в том, который принимает ParsePair.
func (p Pair) String() string {
	return string(p.Base) + "/" + string(p.Quote)
}

// Ошибки валидации. Проверять их нужно через errors.Is, потому что
// в тексте они дополняются самим значением, которое не прошло проверку.
var (
	ErrInvalidCurrency     = errors.New("invalid currency code")
	ErrInvalidPair         = errors.New("invalid pair, expected BASE/QUOTE")
	ErrSameCurrency        = errors.New("base and quote must differ")
	ErrUnsupportedCurrency = errors.New("unsupported currency")
)

// Currencies — набор кодов валют, которые принимает сервис.
type Currencies map[Currency]struct{}

// NewCurrencies собирает набор из уже проверенных кодов.
func NewCurrencies(codes ...Currency) Currencies {
	set := make(Currencies, len(codes))
	for _, c := range codes {
		set[c] = struct{}{}
	}
	return set
}

// Has сообщает, есть ли валюта в наборе.
func (c Currencies) Has(cur Currency) bool {
	_, ok := c[cur]
	return ok
}

// ParseCurrency убирает пробелы по краям, приводит код к верхнему регистру и проверяет его:
// это ровно три латинские буквы.
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

// ParsePair разбирает строку вида "EUR/MXN" и проверяет, что обе валюты разрешены.
// Регистр и пробелы по краям не важны.
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
