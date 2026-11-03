package config

import (
	"testing"
	"time"
)

func TestLoad_WorkerOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://localhost/quotes")
	for key, value := range map[string]string{
		"WORKER_COUNT": "2", "MAX_ATTEMPTS": "4", "POLL_INTERVAL": "500ms",
		"RETRY_BASE_DELAY": "2s", "RETRY_MAX_DELAY": "20s", "QUOTE_TIMEOUT": "4s",
		"DB_TIMEOUT": "2s", "LEASE_DURATION": "15s", "SHUTDOWN_TIMEOUT": "8s",
	} {
		t.Setenv(key, value)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	o := cfg.Worker
	if o.Count != 2 || o.MaxAttempts != 4 || o.PollInterval != 500*time.Millisecond || o.RetryBaseDelay != 2*time.Second || o.RetryMaxDelay != 20*time.Second || o.FetchTimeout != 4*time.Second || o.DBTimeout != 2*time.Second || o.Lease != 15*time.Second || cfg.ShutdownTimeout != 8*time.Second {
		t.Fatalf("config=%+v", cfg)
	}
}

func TestLoad_InvalidWorkerSettings(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"WORKER_COUNT", "two"}, {"WORKER_COUNT", "0"}, {"MAX_ATTEMPTS", "0"},
		{"MAX_ATTEMPTS", "2147483648"}, {"POLL_INTERVAL", "1"}, {"POLL_INTERVAL", "0s"},
		{"QUOTE_TIMEOUT", "-1s"}, {"DB_TIMEOUT", "0s"}, {"LEASE_DURATION", "11s"},
		{"RETRY_BASE_DELAY", "1ns"}, {"RETRY_MAX_DELAY", "500ms"},
		{"SHUTDOWN_TIMEOUT", "bad"}, {"SHUTDOWN_TIMEOUT", "3s"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DATABASE_URL", "postgres://localhost/quotes")
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("expected config error")
			}
		})
	}
}
