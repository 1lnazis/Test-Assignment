package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/domain"
	"github.com/shopspring/decimal"
)

func TestWorker_LogsConfirmedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fetchErr error
		message  string
		level    string
	}{
		{"success", nil, "job completed", "INFO"},
		{"retry", temporaryError(true), "job retry scheduled", "WARN"},
		{"failure", temporaryError(false), "job failed", "ERROR"},
	} {
		for _, outcome := range []string{"saved", "stale", "write error"} {
			t.Run(tc.name+"/"+outcome, func(t *testing.T) {
				var output bytes.Buffer
				writeErr := errors.New("database unavailable")
				write := func() (bool, error) {
					if bytes.Contains(output.Bytes(), []byte(tc.message)) {
						t.Fatal("outcome logged before persistence")
					}
					switch outcome {
					case "stale":
						return false, nil
					case "write error":
						return false, writeErr
					default:
						return true, nil
					}
				}
				q := fakeQueue{
					claim: func(context.Context, int, time.Duration) (Job, bool, error) {
						return jobForTest(1), true, nil
					},
					complete: func(context.Context, Job, decimal.Decimal, string) (bool, error) { return write() },
					retry:    func(context.Context, Job, string, time.Duration) (bool, error) { return write() },
					fail:     func(context.Context, Job, string) (bool, error) { return write() },
				}
				w := testWorker(t, q, func(context.Context, domain.Pair) (decimal.Decimal, error) {
					return decimal.NewFromInt(21), tc.fetchErr
				})
				w.log = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
				worked, err := w.step(context.Background(), 2)
				if !worked || (outcome == "write error") != errors.Is(err, writeErr) {
					t.Fatalf("step: worked=%v, error=%v", worked, err)
				}
				lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
				if len(lines) == 0 {
					t.Fatal("missing claim event")
				}
				for i, line := range lines {
					var event map[string]any
					if err := json.Unmarshal(line, &event); err != nil {
						t.Fatal(err)
					}
					if i == 0 && (event["msg"] != "job claimed" || event["level"] != "DEBUG") {
						t.Fatalf("claim event: %v", event)
					}
					if event["msg"] != "job claimed" && event["msg"] != tc.message {
						continue
					}
					if event["worker"] != float64(2) || event["job_id"] != "job" ||
						event["pair"] != "EUR/MXN" || event["attempt"] != float64(1) {
						t.Fatalf("missing job context: %v", event)
					}
					if event["msg"] == tc.message {
						if event["level"] != tc.level {
							t.Fatalf("outcome level: %v", event)
						}
						if duration, ok := event["duration_ms"].(float64); !ok || duration < 0 {
							t.Fatalf("outcome duration: %v", event)
						}
						if tc.fetchErr == nil && event["provider"] != "test-provider" {
							t.Fatalf("provider: %v", event)
						}
						if tc.fetchErr != nil && event["error"] != tc.fetchErr.Error() {
							t.Fatalf("failure reason: %v", event)
						}
						if tc.name == "retry" && event["retry_delay"] != "1s" {
							t.Fatalf("retry delay: %v", event)
						}
					}
				}
				want := 0
				if outcome == "saved" {
					want = 1
				}
				if got := bytes.Count(output.Bytes(), []byte(tc.message)); got != want {
					t.Fatalf("outcome count=%d, logs=%s", got, output.String())
				}
			})
		}
	}
}
