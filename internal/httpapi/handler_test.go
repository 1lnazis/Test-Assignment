package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/1lnazis/Test-Assignment/internal/domain"
)

// fakeService — заглушка сервиса: каждый тест задаёт нужное поведение функцией.
type fakeService struct {
	requestUpdate func(ctx context.Context, rawPair string) (domain.Update, error)
	getUpdate     func(ctx context.Context, id string) (domain.Update, error)
	latestQuote   func(ctx context.Context, rawPair string) (domain.Quote, error)
}

func (f fakeService) RequestUpdate(ctx context.Context, p string) (domain.Update, error) {
	return f.requestUpdate(ctx, p)
}

func (f fakeService) GetUpdate(ctx context.Context, id string) (domain.Update, error) {
	return f.getUpdate(ctx, id)
}

func (f fakeService) LatestQuote(ctx context.Context, p string) (domain.Quote, error) {
	return f.latestQuote(ctx, p)
}

func do(t *testing.T, svc Service, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	New(svc, log).Routes().ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response is not JSON: %v, body: %s", err, rec.Body.String())
	}
	return m
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	errObj, _ := decodeBody(t, rec)["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	return code
}

const testID = "11111111-1111-4111-8111-111111111111"

func TestRequestUpdate_Accepted(t *testing.T) {
	var gotPair string
	svc := fakeService{requestUpdate: func(_ context.Context, p string) (domain.Update, error) {
		gotPair = p
		return domain.Update{ID: testID}, nil
	}}

	rec := do(t, svc, http.MethodPost, "/api/v1/quotes/updates", `{"pair":"EUR/MXN"}`)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if gotPair != "EUR/MXN" {
		t.Fatalf("service got pair %q", gotPair)
	}
	if got := decodeBody(t, rec)["id"]; got != testID {
		t.Fatalf("id = %v, want %s", got, testID)
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/quotes/updates/"+testID {
		t.Fatalf("Location = %q", loc)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestRequestUpdate_BadBody(t *testing.T) {
	svc := fakeService{requestUpdate: func(context.Context, string) (domain.Update, error) {
		t.Fatal("service must not be called for a malformed body")
		return domain.Update{}, nil
	}}

	tests := map[string]string{
		"not json":       `pair=EUR/MXN`,
		"empty body":     ``,
		"unknown field":  `{"pair":"EUR/MXN","extra":1}`,
		"wrong type":     `{"pair":42}`,
		"too large":      `{"pair":"` + strings.Repeat("A", 2048) + `"}`,
		"second json":    `{"pair":"EUR/MXN"}{"extra":true}`,
		"trailing junk":  `{"pair":"EUR/MXN"}garbage`,
		"oversized tail": `{"pair":"EUR/MXN"}` + strings.Repeat(" ", 2048),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			rec := do(t, svc, http.MethodPost, "/api/v1/quotes/updates", body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if code := errorCode(t, rec); code != "invalid_request" {
				t.Fatalf("code = %q, want invalid_request", code)
			}
		})
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"invalid pair", fmt.Errorf("%w: %q", domain.ErrInvalidPair, "x"), 400, "invalid_pair"},
		{"same currency", domain.ErrSameCurrency, 400, "same_currency"},
		{"unsupported currency", domain.ErrUnsupportedCurrency, 400, "unsupported_currency"},
		{"invalid id", domain.ErrInvalidID, 400, "invalid_id"},
		{"not found", domain.ErrNotFound, 404, "not_found"},
		{"internal", errors.New("connection refused to 10.0.0.5"), 500, "internal_error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := fakeService{requestUpdate: func(context.Context, string) (domain.Update, error) {
				return domain.Update{}, tc.err
			}}
			rec := do(t, svc, http.MethodPost, "/api/v1/quotes/updates", `{"pair":"EUR/MXN"}`)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if code := errorCode(t, rec); code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
			if tc.wantStatus == 500 && strings.Contains(rec.Body.String(), "10.0.0.5") {
				t.Fatalf("internal error details leaked to the client: %s", rec.Body.String())
			}
		})
	}
}

func TestGetUpdate_Pending(t *testing.T) {
	svc := fakeService{getUpdate: func(_ context.Context, id string) (domain.Update, error) {
		return domain.Update{ID: id, Pair: domain.Pair{Base: "EUR", Quote: "MXN"}, Status: domain.StatusPending}, nil
	}}

	rec := do(t, svc, http.MethodGet, "/api/v1/quotes/updates/"+testID, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	m := decodeBody(t, rec)
	if m["status"] != "pending" || m["pair"] != "EUR/MXN" || m["id"] != testID {
		t.Fatalf("unexpected body: %v", m)
	}
	for _, key := range []string{"price", "updated_at", "provider", "error"} {
		if _, ok := m[key]; ok {
			t.Errorf("pending response must not contain %q: %v", key, m)
		}
	}
}

func TestGetUpdate_Done(t *testing.T) {
	price := decimal.RequireFromString("21.4567")
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	svc := fakeService{getUpdate: func(_ context.Context, id string) (domain.Update, error) {
		return domain.Update{
			ID: id, Pair: domain.Pair{Base: "EUR", Quote: "MXN"}, Status: domain.StatusDone,
			Price: &price, Provider: "frankfurter", UpdatedAt: at,
		}, nil
	}}

	m := decodeBody(t, do(t, svc, http.MethodGet, "/api/v1/quotes/updates/"+testID, ""))

	if m["status"] != "done" || m["price"] != "21.4567" || m["provider"] != "frankfurter" {
		t.Fatalf("unexpected body: %v", m)
	}
	if m["updated_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("updated_at = %v", m["updated_at"])
	}
}

func TestGetUpdate_Failed(t *testing.T) {
	svc := fakeService{getUpdate: func(_ context.Context, id string) (domain.Update, error) {
		return domain.Update{ID: id, Pair: domain.Pair{Base: "EUR", Quote: "MXN"}, Status: domain.StatusFailed, Error: "all providers failed"}, nil
	}}

	m := decodeBody(t, do(t, svc, http.MethodGet, "/api/v1/quotes/updates/"+testID, ""))

	if m["status"] != "failed" || m["error"] != "all providers failed" {
		t.Fatalf("unexpected body: %v", m)
	}
	if _, ok := m["price"]; ok {
		t.Fatalf("failed response must not contain price: %v", m)
	}
}

func TestGetUpdate_PassesPathID(t *testing.T) {
	var got string
	svc := fakeService{getUpdate: func(_ context.Context, id string) (domain.Update, error) {
		got = id
		return domain.Update{}, domain.ErrNotFound
	}}

	rec := do(t, svc, http.MethodGet, "/api/v1/quotes/updates/"+testID, "")

	if got != testID {
		t.Fatalf("service got id %q, want %q", got, testID)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestLatestQuote(t *testing.T) {
	var gotPair string
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	svc := fakeService{latestQuote: func(_ context.Context, p string) (domain.Quote, error) {
		gotPair = p
		return domain.Quote{
			Pair: domain.Pair{Base: "EUR", Quote: "MXN"}, Price: decimal.RequireFromString("21.5"),
			Provider: "open-er-api", UpdatedAt: at,
		}, nil
	}}

	rec := do(t, svc, http.MethodGet, "/api/v1/quotes/latest?pair=EUR/MXN", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPair != "EUR/MXN" {
		t.Fatalf("service got pair %q", gotPair)
	}
	m := decodeBody(t, rec)
	if m["pair"] != "EUR/MXN" || m["price"] != "21.5" || m["provider"] != "open-er-api" || m["updated_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("unexpected body: %v", m)
	}
}

func TestLatestQuote_NotFound(t *testing.T) {
	svc := fakeService{latestQuote: func(context.Context, string) (domain.Quote, error) {
		return domain.Quote{}, domain.ErrNotFound
	}}

	rec := do(t, svc, http.MethodGet, "/api/v1/quotes/latest?pair=EUR/MXN", "")

	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
		t.Fatalf("status = %d, code = %q", rec.Code, errorCode(t, rec))
	}
}

func TestLatestQuote_MissingPairParam(t *testing.T) {
	// Пустой pair доходит до сервиса, и тот отвечает ErrInvalidPair.
	svc := fakeService{latestQuote: func(_ context.Context, p string) (domain.Quote, error) {
		if p != "" {
			t.Fatalf("pair = %q, want empty", p)
		}
		return domain.Quote{}, fmt.Errorf("%w: %q", domain.ErrInvalidPair, p)
	}}

	rec := do(t, svc, http.MethodGet, "/api/v1/quotes/latest", "")

	if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_pair" {
		t.Fatalf("status = %d, code = %q", rec.Code, errorCode(t, rec))
	}
}

func TestMethodNotAllowed(t *testing.T) {
	rec := do(t, fakeService{}, http.MethodDelete, "/api/v1/quotes/updates", "")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "POST") {
		t.Fatalf("Allow = %q, want it to list POST", allow)
	}
}

func TestRecoverer(t *testing.T) {
	svc := fakeService{latestQuote: func(context.Context, string) (domain.Quote, error) {
		panic("boom")
	}}

	rec := do(t, svc, http.MethodGet, "/api/v1/quotes/latest?pair=EUR/MXN", "")

	if rec.Code != http.StatusInternalServerError || errorCode(t, rec) != "internal_error" {
		t.Fatalf("status = %d, code = %q", rec.Code, errorCode(t, rec))
	}
}
