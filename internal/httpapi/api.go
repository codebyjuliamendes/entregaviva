// Package httpapi exposes the JSON API, the SSE streams and the static frontend.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/codebyjuliamendes/entregaviva/internal/config"
	"github.com/codebyjuliamendes/entregaviva/internal/events"
	"github.com/codebyjuliamendes/entregaviva/internal/store"
)

// User-facing messages (pt-BR), kept compatible with the previous backend.
const (
	msgInternal       = "Erro interno. Tente novamente em instantes."
	msgUnauthorized   = "Acesso restrito à equipe de operação."
	msgRateLimited    = "Muitas consultas seguidas. Aguarde um instante e tente novamente."
	msgAPINotFound    = "Rota da API não encontrada."
	msgMethod         = "Método não permitido para esta rota."
	msgBadBody        = "Corpo da requisição inválido. Envie JSON."
	msgBodyTooLarge   = "Corpo da requisição muito grande."
	msgInvalidOrder   = "Confira restaurante, cliente, endereço e valor do pedido."
	msgOrderNotFound  = "Pedido não encontrado."
	msgTrackNotFound  = "Pedido não encontrado. Confira o código de acompanhamento."
	msgInvalidStatus  = "Status inválido."
	msgConflict       = "O pedido mudou em outra sessão; atualize o painel."
	msgStreamBusy     = "Muitas conexões abertas. Tente novamente em instantes."
	msgUnavailable    = "Serviço indisponível."
	maxBodyBytes      = 20 << 10
	streamRetryMillis = 3000
)

// Deps are the collaborators of the HTTP layer.
type Deps struct {
	Store  *store.Store
	Hub    *events.Hub
	Log    *slog.Logger
	Config config.Config
	Static fs.FS            // frontend files (public/)
	Now    func() time.Time // defaults to time.Now
}

type api struct {
	store        *store.Store
	hub          *events.Hub
	log          *slog.Logger
	cfg          config.Config
	now          func() time.Time
	trackLimiter *rateLimiter
}

// New builds the root handler.
func New(d Deps) (http.Handler, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	a := &api{store: d.Store, hub: d.Hub, log: d.Log, cfg: d.Config, now: d.Now}
	a.trackLimiter = newRateLimiter(d.Config.TrackRatePerMin, time.Minute, d.Now)
	static, err := newStaticHandler(d.Static)
	if err != nil {
		return nil, err
	}

	r := chi.NewRouter()
	r.Use(requestID, a.accessLog, a.securityHeaders)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { writeError(w, http.StatusNotFound, msgAPINotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) { writeError(w, http.StatusMethodNotAllowed, msgMethod) })

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", a.health)
		r.Head("/health", a.health)

		// Public customer tracking: rate limited, privacy-safe projection.
		r.Group(func(r chi.Router) {
			r.Use(a.rateLimit)
			r.Get("/orders/track/{code}", a.trackOrder)
			r.Get("/orders/track/{code}/events", a.trackStream)
		})

		// Operations panel.
		r.Group(func(r chi.Router) {
			r.Use(a.operatorAuth)
			r.Get("/orders", a.listOrders)
			r.Post("/orders", a.createOrder)
			r.Patch("/orders/{id}/status", a.changeStatus)
			r.Get("/events", a.panelStream)
		})
	})

	// Frontend. The panel page and its script require operator auth; the
	// tracking page and shared assets are public.
	r.Group(func(r chi.Router) {
		r.Use(a.operatorAuth)
		r.Get("/", static.ServeHTTP)
		r.Get("/index.html", static.ServeHTTP)
		r.Get("/app.js", static.ServeHTTP)
	})
	r.Get("/*", static.ServeHTTP)
	r.Head("/*", static.ServeHTTP)
	return r, nil
}

func (a *api) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		a.log.ErrorContext(r.Context(), "health check failed", "request_id", RequestID(r.Context()), "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "error": msgUnavailable})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": a.now().UTC(), "streams": a.hub.Len()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errorBody struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields,omitempty"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// internalError logs the cause and answers with a generic message.
func (a *api) internalError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return // client went away
	}
	a.log.ErrorContext(r.Context(), "internal error", "request_id", RequestID(r.Context()), "err", err)
	writeError(w, http.StatusInternalServerError, msgInternal)
}
