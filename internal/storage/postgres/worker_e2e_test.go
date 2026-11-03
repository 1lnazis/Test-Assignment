package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/domain"
	"github.com/1lnazis/Test-Assignment/internal/httpapi"
	"github.com/1lnazis/Test-Assignment/internal/provider/frankfurter"
	"github.com/1lnazis/Test-Assignment/internal/service"
	"github.com/1lnazis/Test-Assignment/internal/worker"
	"github.com/shopspring/decimal"
)

type observedQueue struct {
	*Repo
	terminal chan error
	delays   []time.Duration
}

func (q *observedQueue) Retry(ctx context.Context, j worker.Job, reason string, d time.Duration) (bool, error) {
	q.delays = append(q.delays, d)
	return q.Repo.Retry(ctx, j, reason, 0)
}
func (q *observedQueue) Complete(ctx context.Context, j worker.Job, p decimal.Decimal, name string) (bool, error) {
	saved, err := q.Repo.Complete(ctx, j, p, name)
	q.notify(saved, err)
	return saved, err
}
func (q *observedQueue) Fail(ctx context.Context, j worker.Job, reason string) (bool, error) {
	saved, err := q.Repo.Fail(ctx, j, reason)
	q.notify(saved, err)
	return saved, err
}
func (q *observedQueue) notify(saved bool, err error) {
	if err == nil && !saved {
		err = fmt.Errorf("terminal write did not save")
	}
	q.terminal <- err
}

func TestWorker_HTTPToPostgresAndMockProvider(t *testing.T) {
	for _, tc := range []struct {
		name                string
		failures            int
		status              int
		malformed, previous bool
		wantStatus          string
		wantAttempts        int
	}{
		{"success", 0, 0, false, false, "done", 1},
		{"retry then success", 1, 503, false, false, "done", 2},
		{"retry budget exhausted", 3, 503, false, false, "failed", 3},
		{"permanent failure", 1, 422, false, false, "failed", 1},
		{"malformed response", 0, 0, true, false, "failed", 1},
		{"old quote survives failure", 1, 422, false, true, "failed", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newTestPool(t)
			repo := NewRepo(pool)
			if tc.previous {
				queueExec(t, pool, `INSERT INTO quote_updates(base,quote,status,price,provider,updated_at) VALUES('EUR','MXN','done',20,'old',now()-interval '1 day')`)
			}
			var calls atomic.Int32
			providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v2/rate/eur/mxn" {
					t.Errorf("unexpected provider request: %s %s", r.Method, r.URL.Path)
				}
				n := calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if int(n) <= tc.failures {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, `{"message":"provider error"}`)
					return
				}
				if tc.malformed {
					_, _ = io.WriteString(w, `{"rate":`)
					return
				}
				_, _ = io.WriteString(w, `{"date":"2026-11-02","base":"EUR","quote":"MXN","rate":21.07212345675}`)
			}))
			defer providerServer.Close()
			client, err := frankfurter.New(providerServer.URL, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			handler := httpapi.New(service.New(repo, domain.NewCurrencies("EUR", "MXN")), log).Routes()
			request := httptest.NewRequest("POST", "/api/v1/quotes/updates", strings.NewReader(`{"pair":"EUR/MXN"}`))
			request.Header.Set("Content-Type", "application/json")
			accepted := httptest.NewRecorder()
			handler.ServeHTTP(accepted, request)
			if accepted.Code != 202 {
				t.Fatalf("POST: %d %s", accepted.Code, accepted.Body)
			}
			var created struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(accepted.Body.Bytes(), &created); err != nil || created.ID == "" {
				t.Fatalf("created=%+v err=%v", created, err)
			}
			if accepted.Header().Get("Location") != "/api/v1/quotes/updates/"+created.ID {
				t.Fatal("missing Location")
			}
			pending, err := repo.GetUpdate(context.Background(), created.ID)
			if err != nil || pending.Status != domain.StatusPending {
				t.Fatalf("initial state=%+v err=%v", pending, err)
			}
			queue := &observedQueue{Repo: repo, terminal: make(chan error, 1)}
			opts := worker.DefaultOptions()
			opts.PollInterval = time.Hour
			background, err := worker.New(queue, client, log, opts)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- background.Run(ctx) }()
			select {
			case err := <-queue.terminal:
				if err != nil {
					cancel()
					t.Error(err)
				}
			case <-ctx.Done():
				t.Error("worker did not produce terminal result")
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if int(calls.Load()) != tc.wantAttempts {
				t.Fatalf("provider calls=%d want=%d", calls.Load(), tc.wantAttempts)
			}
			var attempts int
			var released bool
			if err := pool.QueryRow(context.Background(), `SELECT attempts,locked_until IS NULL FROM quote_updates WHERE id=$1`, created.ID).Scan(&attempts, &released); err != nil {
				t.Fatal(err)
			}
			if attempts != tc.wantAttempts || !released {
				t.Fatalf("attempts=%d released=%v", attempts, released)
			}
			for n, d := range queue.delays {
				if want := time.Second << n; d != want {
					t.Errorf("delay[%d]=%v want=%v", n, d, want)
				}
			}
			if len(queue.delays) != tc.wantAttempts-1 {
				t.Fatalf("retry count=%d", len(queue.delays))
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/quotes/updates/"+created.ID, nil))
			var got map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || got["status"] != tc.wantStatus {
				t.Fatalf("GET: %d %s", response.Code, response.Body)
			}
			if tc.wantStatus == "done" {
				if got["price"] != "21.0721234568" || got["provider"] != "frankfurter" || got["error"] != "" {
					t.Fatalf("done result=%v", got)
				}
				if _, err := time.Parse(time.RFC3339Nano, got["updated_at"]); err != nil {
					t.Fatalf("updated_at: %v", err)
				}
			} else if got["error"] == "" || got["price"] != "" || got["updated_at"] != "" {
				t.Fatalf("failed result=%v", got)
			}
			latest := httptest.NewRecorder()
			handler.ServeHTTP(latest, httptest.NewRequest("GET", "/api/v1/quotes/latest?pair=EUR%2FMXN", nil))
			if tc.wantStatus == "done" || tc.previous {
				var quote map[string]string
				if err := json.Unmarshal(latest.Body.Bytes(), &quote); err != nil {
					t.Fatal(err)
				}
				wantPrice := "21.0721234568"
				if tc.previous {
					wantPrice = "20"
				}
				if latest.Code != 200 || quote["price"] != wantPrice {
					t.Fatalf("latest: %d %s", latest.Code, latest.Body)
				}
			} else if latest.Code != 404 {
				t.Fatalf("latest without success: %d %s", latest.Code, latest.Body)
			}
		})
	}
}
