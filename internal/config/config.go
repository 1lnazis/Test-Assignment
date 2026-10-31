// Package config загружает настройки сервиса из переменных окружения.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/1lnazis/Test-Assignment/internal/domain"
)

// Config — все настройки сервиса.
type Config struct {
	// HTTPAddr — адрес, на котором слушает HTTP API.
	HTTPAddr string
	// DatabaseURL — строка подключения к Postgres.
	DatabaseURL string
	// Currencies — набор валют, которые принимает сервис.
	Currencies domain.Currencies
	// LogLevel — минимальный уровень сообщений, которые пишет логгер.
	LogLevel slog.Level
}

const (
	defaultHTTPAddr   = ":8080"
	defaultCurrencies = "USD,EUR,MXN,RUB,KZT"
	defaultLogLevel   = "info"
)

// Load читает конфиг из окружения. Если переменная не задана или пуста,
// берётся значение по умолчанию. Исключение — DATABASE_URL: она обязательна.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:    env("HTTP_ADDR", defaultHTTPAddr),
		DatabaseURL: os.Getenv("DATABASE_URL"),
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}

	currencies, err := parseCurrencies(env("CURRENCIES", defaultCurrencies))
	if err != nil {
		return Config{}, fmt.Errorf("CURRENCIES: %w", err)
	}
	cfg.Currencies = currencies

	if err := cfg.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", defaultLogLevel))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
	}

	return cfg, nil
}

// parseCurrencies разбирает список вида "USD,EUR,MXN" в набор валют.
// Нужно минимум две разные валюты, иначе не из чего составить пару.
func parseCurrencies(raw string) (domain.Currencies, error) {
	parts := strings.Split(raw, ",")
	codes := make([]domain.Currency, 0, len(parts))
	for _, part := range parts {
		cur, err := domain.ParseCurrency(part)
		if err != nil {
			return nil, err
		}
		codes = append(codes, cur)
	}

	set := domain.NewCurrencies(codes...)
	if len(set) < 2 {
		return nil, errors.New("need at least two different currencies")
	}
	return set, nil
}

// env возвращает значение переменной окружения или fallback, если она пуста.
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
