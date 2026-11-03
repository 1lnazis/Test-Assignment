package config

import (
	"github.com/1lnazis/Test-Assignment/internal/worker"
	"log/slog"
	"testing"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/domain"
)

// clearEnv обнуляет все переменные, которые читает Load, чтобы тесты не зависели от окружения компьютера.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"HTTP_ADDR", "DATABASE_URL", "CURRENCIES", "LOG_LEVEL", "WORKER_COUNT", "MAX_ATTEMPTS", "POLL_INTERVAL", "RETRY_BASE_DELAY", "RETRY_MAX_DELAY", "QUOTE_TIMEOUT", "DB_TIMEOUT", "LEASE_DURATION", "SHUTDOWN_TIMEOUT"} {
		t.Setenv(k, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://localhost/quotes")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.DatabaseURL != "postgres://localhost/quotes" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	for _, c := range []domain.Currency{"USD", "EUR", "MXN", "RUB", "KZT"} {
		if !cfg.Currencies.Has(c) {
			t.Errorf("default currencies miss %s", c)
		}
	}
	if cfg.Worker != worker.DefaultOptions() || cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("worker defaults: %+v shutdown %v", cfg.Worker, cfg.ShutdownTimeout)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
}

func TestLoad_Overrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://db/q")
	t.Setenv("HTTP_ADDR", "127.0.0.1:9000")
	t.Setenv("CURRENCIES", "usd, gbp")
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.HTTPAddr != "127.0.0.1:9000" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if len(cfg.Currencies) != 2 || !cfg.Currencies.Has("USD") || !cfg.Currencies.Has("GBP") {
		t.Errorf("Currencies = %v, want USD and GBP", cfg.Currencies)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
}

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"missing database url", map[string]string{}},
		{"bad currency code", map[string]string{"DATABASE_URL": "x", "CURRENCIES": "USD,EU"}},
		{"trailing comma", map[string]string{"DATABASE_URL": "x", "CURRENCIES": "USD,EUR,"}},
		{"single currency", map[string]string{"DATABASE_URL": "x", "CURRENCIES": "USD"}},
		{"duplicate currencies", map[string]string{"DATABASE_URL": "x", "CURRENCIES": "USD,usd"}},
		{"bad log level", map[string]string{"DATABASE_URL": "x", "LOG_LEVEL": "loud"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Fatal("Load: expected error, got nil")
			}
		})
	}
}
