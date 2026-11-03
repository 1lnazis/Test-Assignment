package frankfurter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/domain"
	"github.com/shopspring/decimal"
)

const DefaultBaseURL = "https://api.frankfurter.dev"
const maxResponseBytes = 64 << 10

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func New(baseURL string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("frankfurter: parse base URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, fmt.Errorf("frankfurter: base URL must have an HTTP(S) scheme and host, without query or fragment")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("frankfurter: timeout must be positive")
	}
	return &Client{
		baseURL:    strings.TrimRight(u.String(), "/"),
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

func (c *Client) Name() string { return "frankfurter" }

type rateResponse struct {
	Base  string      `json:"base"`
	Quote string      `json:"quote"`
	Rate  json.Number `json:"rate"`
}

func (c *Client) Fetch(ctx context.Context, pair domain.Pair) (decimal.Decimal, error) {
	base, err := domain.ParseCurrency(string(pair.Base))
	if err != nil {
		return decimal.Zero, fmt.Errorf("frankfurter: base currency: %w", err)
	}
	quote, err := domain.ParseCurrency(string(pair.Quote))
	if err != nil {
		return decimal.Zero, fmt.Errorf("frankfurter: quote currency: %w", err)
	}
	if base == quote {
		return decimal.Zero, fmt.Errorf("frankfurter: %w", domain.ErrSameCurrency)
	}

	endpoint := c.baseURL + "/v2/rate/" +
		strings.ToLower(string(base)) + "/" + strings.ToLower(string(quote))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return decimal.Zero, fmt.Errorf("frankfurter: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return decimal.Zero, fmt.Errorf("frankfurter: send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return decimal.Zero, fmt.Errorf("frankfurter: read response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return decimal.Zero, fmt.Errorf("frankfurter: response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		var apiError struct {
			Message string `json:"message"`
		}

		_ = json.Unmarshal(body, &apiError)
		return decimal.Zero, &HTTPError{StatusCode: resp.StatusCode, Message: apiError.Message}
	}

	var data rateResponse

	if err := json.Unmarshal(body, &data); err != nil {
		return decimal.Zero, fmt.Errorf("frankfurter: decode response: %w", err)
	}
	if data.Base != string(base) || data.Quote != string(quote) {
		return decimal.Zero, fmt.Errorf("frankfurter: unexpected pair %q/%q, want %s/%s",
			data.Base, data.Quote, base, quote)
	}
	price, err := decimal.NewFromString(data.Rate.String())
	if err != nil {
		return decimal.Zero, fmt.Errorf("frankfurter: parse rate: %w", err)
	}
	if !price.IsPositive() {
		return decimal.Zero, fmt.Errorf("frankfurter: rate must be positive")
	}
	return price, nil
}

type HTTPError struct {
	StatusCode int
	Message    string
}

func (e *HTTPError) Error() string {
	message := e.Message
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("frankfurter: HTTP %d: %s", e.StatusCode, message)
}

func (e *HTTPError) Retryable() bool {
	return e.StatusCode == http.StatusRequestTimeout ||
		e.StatusCode == http.StatusTooManyRequests ||
		(e.StatusCode >= 500 && e.StatusCode <= 599)
}
