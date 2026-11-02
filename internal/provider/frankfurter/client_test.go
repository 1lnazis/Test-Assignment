package frankfurter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/domain"
)

// Подменяем только HTTP-транспорт: запросы и ответы проходят через http.Client,
// но для тестов не нужны интернет и работающий Frankfurter.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestClient(t *testing.T, transport roundTripFunc) *Client {
	t.Helper()
	c, err := New("https://example.test/", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	c.httpClient.Transport = transport
	return c
}

func response(r *http.Request, status int, body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       body,
		Request:    r,
	}
}

func testPair() domain.Pair { return domain.Pair{Base: "EUR", Quote: "MXN"} }

func TestNew(t *testing.T) {
	c, err := New(" https://example.test/prefix/// ", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != "https://example.test/prefix" || c.httpClient.Timeout != time.Second {
		t.Fatalf("unexpected settings: URL=%q, timeout=%s", c.baseURL, c.httpClient.Timeout)
	}
	if c.Name() != "frankfurter" {
		t.Fatalf("Name() = %q", c.Name())
	}
	for _, raw := range []string{"", "example.test", "ftp://example.test", "https://", "https://:80", "https://example.test?key=value", "https://example.test?", "https://example.test#part", "://bad"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := New(raw, time.Second); err == nil {
				t.Fatalf("New(%q) must fail", raw)
			}
		})
	}
	for _, timeout := range []time.Duration{0, -time.Second} {
		if _, err := New(DefaultBaseURL, timeout); err == nil {
			t.Fatalf("New with timeout %s must fail", timeout)
		}
	}
}

type contextKey struct{}

func TestFetch_RequestAndExactPrice(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.String() != "https://example.test/v2/rate/eur/mxn" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		if r.Context().Value(contextKey{}) != "request-value" {
			t.Error("caller context did not reach HTTP request")
		}
		body := `{"date":"2026-11-02","base":"EUR","quote":"MXN","rate":21.072000000123456789,"future_field":true}`
		return response(r, http.StatusOK, io.NopCloser(strings.NewReader(body))), nil
	})
	ctx := context.WithValue(context.Background(), contextKey{}, "request-value")
	price, err := c.Fetch(ctx, testPair())
	if err != nil {
		t.Fatal(err)
	}
	if price.String() != "21.072000000123456789" {
		t.Fatalf("price = %s; precision was lost", price)
	}
	if calls != 1 {
		t.Fatalf("Fetch performed %d attempts, want 1", calls)
	}
}

func TestFetch_InvalidResponse(t *testing.T) {
	cases := map[string]string{
		"invalid JSON":   `<html>bad gateway</html>`,
		"trailing JSON":  `{"base":"EUR","quote":"MXN","rate":21}{}`,
		"wrong base":     `{"base":"USD","quote":"MXN","rate":21}`,
		"wrong quote":    `{"base":"EUR","quote":"USD","rate":21}`,
		"missing pair":   `{"rate":21}`,
		"missing rate":   `{"base":"EUR","quote":"MXN"}`,
		"null rate":      `{"base":"EUR","quote":"MXN","rate":null}`,
		"zero rate":      `{"base":"EUR","quote":"MXN","rate":0}`,
		"negative rate":  `{"base":"EUR","quote":"MXN","rate":-1}`,
		"invalid number": `{"base":"EUR","quote":"MXN","rate":"NaN"}`,
		"too large":      strings.Repeat(" ", maxResponseBytes+1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
				return response(r, http.StatusOK, io.NopCloser(strings.NewReader(body))), nil
			})
			if _, err := c.Fetch(context.Background(), testPair()); err == nil {
				t.Fatal("invalid provider response was accepted")
			}
		})
	}
}

func TestFetch_HTTPError(t *testing.T) {
	for _, tc := range []struct {
		status    int
		body      string
		retryable bool
	}{
		{400, `{"status":400,"message":"bad request"}`, false},
		{404, `{"status":404,"message":"rate not found"}`, false},
		{422, `{"status":422,"message":"invalid currency"}`, false},
		{408, `{"message":"request timeout"}`, true},
		{429, `{"message":"too many requests"}`, true},
		{500, `<html>server error</html>`, true},
		{503, `{"message":"temporarily unavailable"}`, true},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			calls := 0
			c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				return response(r, tc.status, io.NopCloser(strings.NewReader(tc.body))), nil
			})
			_, err := c.Fetch(context.Background(), testPair())
			var apiErr *HTTPError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want *HTTPError", err)
			}
			if apiErr.StatusCode != tc.status || apiErr.Retryable() != tc.retryable {
				t.Fatalf("HTTP error = %+v, retryable=%v", apiErr, apiErr.Retryable())
			}
			if !strings.Contains(apiErr.Error(), "frankfurter: HTTP") || apiErr.Error() == "" {
				t.Fatalf("unexpected error message: %q", apiErr.Error())
			}
			if tc.status == 404 && apiErr.Message != "rate not found" {
				t.Fatalf("provider message = %q", apiErr.Message)
			}
			if tc.status == 500 && !strings.Contains(apiErr.Error(), "Internal Server Error") {
				t.Fatalf("no fallback message: %v", apiErr)
			}
			if calls != 1 {
				t.Fatalf("unexpected automatic retry: %d calls", calls)
			}
		})
	}
}

func TestFetch_InvalidPairDoesNotSendRequest(t *testing.T) {
	for _, pair := range []domain.Pair{{}, {Base: "EU", Quote: "MXN"}, {Base: "EUR", Quote: "123"}, {Base: "EUR", Quote: "EUR"}} {
		c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
			t.Fatal("invalid pair reached transport")
			return nil, errors.New("unexpected request")
		})
		if _, err := c.Fetch(context.Background(), pair); err == nil {
			t.Fatalf("pair %+v was accepted", pair)
		}
	}
}

func TestFetch_TransportError(t *testing.T) {
	want := errors.New("connection failed")
	c := newTestClient(t, func(*http.Request) (*http.Response, error) { return nil, want })
	if _, err := c.Fetch(context.Background(), testPair()); !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped transport error", err)
	}
}

func TestFetch_ContextCanceled(t *testing.T) {
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Fetch(ctx, testPair()); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestFetch_Timeout(t *testing.T) {
	c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	c.httpClient.Timeout = 20 * time.Millisecond
	if _, err := c.Fetch(context.Background(), testPair()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

type trackedBody struct {
	reader io.Reader
	closed bool
}

func (b *trackedBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *trackedBody) Close() error               { b.closed = true; return nil }

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestFetch_ClosesBody(t *testing.T) {
	want := errors.New("broken response stream")
	for _, tc := range []struct {
		name    string
		status  int
		reader  io.Reader
		wantErr error
	}{
		{"success", 200, strings.NewReader(`{"base":"EUR","quote":"MXN","rate":21.072}`), nil},
		{"HTTP error", 503, strings.NewReader(`{}`), nil},
		{"invalid JSON", 200, strings.NewReader(`bad`), nil},
		{"read error", 200, errorReader{want}, want},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedBody{reader: tc.reader}
			c := newTestClient(t, func(r *http.Request) (*http.Response, error) {
				return response(r, tc.status, body), nil
			})
			_, err := c.Fetch(context.Background(), testPair())
			if !body.closed {
				t.Error("response body was not closed")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
