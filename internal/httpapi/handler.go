// Package httpapi — HTTP-слой: разбор запросов, вызов сервиса и формирование JSON-ответов.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/1lnazis/Test-Assignment/internal/domain"
)

// maxBodyBytes — предел размера тела запроса. Нужная нам структура крошечная,
// всё сверх этого — мусор или попытка забить память.
const maxBodyBytes = 1 << 10

// Service — то, что HTTP-слою нужно от бизнес-логики.
type Service interface {
	RequestUpdate(ctx context.Context, rawPair string) (domain.Update, error)
	GetUpdate(ctx context.Context, id string) (domain.Update, error)
	LatestQuote(ctx context.Context, rawPair string) (domain.Quote, error)
}

// Handler обслуживает HTTP API.
type Handler struct {
	svc Service
	log *slog.Logger
}

// New создаёт обработчик API.
func New(svc Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// Routes возвращает готовый http.Handler со всеми маршрутами и middleware.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/quotes/updates", h.requestUpdate)
	mux.HandleFunc("GET /api/v1/quotes/updates/{id}", h.getUpdate)
	mux.HandleFunc("GET /api/v1/quotes/latest", h.latestQuote)

	// Порядок важен: recoverer стоит внутри logRequests, поэтому
	// ответ 500 после паники тоже попадёт в лог запросов.
	return h.logRequests(h.recoverer(mux))
}

type requestUpdateBody struct {
	Pair string `json:"pair"`
}

type requestUpdateResponse struct {
	ID string `json:"id"`
}

// requestUpdate — POST /api/v1/quotes/updates. Только ставит запрос в очередь
// и сразу отвечает, а обновление выполнит фоновый воркер.
func (h *Handler) requestUpdate(w http.ResponseWriter, r *http.Request) {
	var body requestUpdateBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be JSON like {\"pair\":\"EUR/MXN\"}")
		return
	}

	u, err := h.svc.RequestUpdate(r.Context(), body.Pair)
	if err != nil {
		h.handleError(w, r, err)
		return
	}

	w.Header().Set("Location", "/api/v1/quotes/updates/"+u.ID)
	writeJSON(w, http.StatusAccepted, requestUpdateResponse{ID: u.ID})
}

type updateResponse struct {
	ID        string     `json:"id"`
	Pair      string     `json:"pair"`
	Status    string     `json:"status"`
	Price     *string    `json:"price,omitempty"`
	Provider  string     `json:"provider,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// getUpdate — GET /api/v1/quotes/updates/{id}. Цена и время есть только у выполненного запроса.
func (h *Handler) getUpdate(w http.ResponseWriter, r *http.Request) {
	u, err := h.svc.GetUpdate(r.Context(), r.PathValue("id"))
	if err != nil {
		h.handleError(w, r, err)
		return
	}

	resp := updateResponse{
		ID:     u.ID,
		Pair:   u.Pair.String(),
		Status: string(u.Status),
		Error:  u.Error,
	}
	if u.Status == domain.StatusDone && u.Price != nil {
		price := u.Price.String()
		updatedAt := u.UpdatedAt.UTC()
		resp.Price = &price
		resp.Provider = u.Provider
		resp.UpdatedAt = &updatedAt
	}
	writeJSON(w, http.StatusOK, resp)
}

type quoteResponse struct {
	Pair      string    `json:"pair"`
	Price     string    `json:"price"`
	Provider  string    `json:"provider,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// latestQuote — GET /api/v1/quotes/latest?pair=EUR/MXN.
// Пара передаётся в query, потому что слэш в EUR/MXN ломает разбор пути.
func (h *Handler) latestQuote(w http.ResponseWriter, r *http.Request) {
	q, err := h.svc.LatestQuote(r.Context(), r.URL.Query().Get("pair"))
	if err != nil {
		h.handleError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, quoteResponse{
		Pair:      q.Pair.String(),
		Price:     q.Price.String(),
		Provider:  q.Provider,
		UpdatedAt: q.UpdatedAt.UTC(),
	})
}

// handleError превращает ошибку сервиса в HTTP-ответ. Ошибки пользователя
// (400, 404) показываем как есть, а про внутренние пишем в лог и отвечаем
// общим текстом, чтобы не раскрывать детали.
func (h *Handler) handleError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidPair):
		writeError(w, http.StatusBadRequest, "invalid_pair", err.Error())
	case errors.Is(err, domain.ErrSameCurrency):
		writeError(w, http.StatusBadRequest, "same_currency", err.Error())
	case errors.Is(err, domain.ErrUnsupportedCurrency):
		writeError(w, http.StatusBadRequest, "unsupported_currency", err.Error())
	case errors.Is(err, domain.ErrInvalidID):
		writeError(w, http.StatusBadRequest, "invalid_id", err.Error())
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	default:
		h.log.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

// decodeJSON читает тело запроса в v: ограничивает размер и не пропускает неизвестные поля.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		return err
	}

	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("request body must contain exactly one JSON value")
	}

	return nil
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: errorBody{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Заголовки уже отправлены, поэтому ошибку записи сделать нечем: клиент ушёл.
	_ = json.NewEncoder(w).Encode(v)
}
