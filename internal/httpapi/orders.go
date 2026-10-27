package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/codebyjuliamendes/entregaviva/internal/domain"
	"github.com/codebyjuliamendes/entregaviva/internal/store"
)

// statusLabels are used in user-facing error messages.
var statusLabels = map[domain.Status]string{
	domain.StatusReceived:  "Recebido",
	domain.StatusPreparing: "Preparando",
	domain.StatusReady:     "Pronto",
	domain.StatusOnRoute:   "Em rota",
	domain.StatusDelivered: "Entregue",
	domain.StatusCanceled:  "Cancelado",
}

type eventJSON struct {
	Status    domain.Status `json:"status"`
	Note      string        `json:"note"`
	CreatedAt time.Time     `json:"createdAt"`
}

// orderJSON is the operator view. Field names match the previous backend.
type orderJSON struct {
	ID                  int64         `json:"id"`
	Code                string        `json:"code"`
	Restaurant          string        `json:"restaurant"`
	Customer            string        `json:"customer"`
	Courier             string        `json:"courier"`
	Address             string        `json:"address"`
	TotalCents          int64         `json:"totalCents"`
	EstimatedMinutes    int           `json:"estimatedMinutes"`
	Status              domain.Status `json:"status"`
	CreatedAt           time.Time     `json:"createdAt"`
	UpdatedAt           time.Time     `json:"updatedAt"`
	EstimatedDeliveryAt time.Time     `json:"estimatedDeliveryAt"`
	RemainingMinutes    int           `json:"remainingMinutes"`
	Events              []eventJSON   `json:"events"`
}

type publicEventJSON struct {
	Status    domain.Status `json:"status"`
	CreatedAt time.Time     `json:"createdAt"`
}

// trackJSON is the public view: no internal id, customer, address, courier,
// total or free-text notes (which operators may fill with personal data).
type trackJSON struct {
	Code                string            `json:"code"`
	Restaurant          string            `json:"restaurant"`
	Status              domain.Status     `json:"status"`
	EstimatedMinutes    int               `json:"estimatedMinutes"`
	CreatedAt           time.Time         `json:"createdAt"`
	UpdatedAt           time.Time         `json:"updatedAt"`
	EstimatedDeliveryAt time.Time         `json:"estimatedDeliveryAt"`
	RemainingMinutes    int               `json:"remainingMinutes"`
	Events              []publicEventJSON `json:"events"`
}

func toOrderJSON(o domain.Order, now time.Time) orderJSON {
	evs := make([]eventJSON, len(o.Events))
	for i, e := range o.Events {
		evs[i] = eventJSON{Status: e.Status, Note: e.Note, CreatedAt: e.CreatedAt.UTC()}
	}
	return orderJSON{
		ID: o.ID, Code: o.Code, Restaurant: o.Restaurant, Customer: o.Customer, Courier: o.Courier,
		Address: o.Address, TotalCents: o.TotalCents, EstimatedMinutes: o.EstimatedMinutes, Status: o.Status,
		CreatedAt: o.CreatedAt.UTC(), UpdatedAt: o.UpdatedAt.UTC(), EstimatedDeliveryAt: o.EstimatedDeliveryAt(),
		RemainingMinutes: o.RemainingMinutes(now), Events: evs,
	}
}

func toTrackJSON(o domain.Order, now time.Time) trackJSON {
	evs := make([]publicEventJSON, len(o.Events))
	for i, e := range o.Events {
		evs[i] = publicEventJSON{Status: e.Status, CreatedAt: e.CreatedAt.UTC()}
	}
	return trackJSON{
		Code: o.Code, Restaurant: o.Restaurant, Status: o.Status, EstimatedMinutes: o.EstimatedMinutes,
		CreatedAt: o.CreatedAt.UTC(), UpdatedAt: o.UpdatedAt.UTC(), EstimatedDeliveryAt: o.EstimatedDeliveryAt(),
		RemainingMinutes: o.RemainingMinutes(now), Events: evs,
	}
}

func (a *api) listOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := a.store.ListOrders(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	now := a.now()
	out := make([]orderJSON, len(orders))
	for i, o := range orders {
		out[i] = toOrderJSON(o, now)
	}
	writeJSON(w, http.StatusOK, out)
}

// flexString accepts a JSON string or number (HTML forms send numbers as strings).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case bytes.Equal(b, []byte("null")):
		*f = ""
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
	default:
		var n json.Number
		if err := json.Unmarshal(b, &n); err != nil {
			return err
		}
		*f = flexString(n.String())
	}
	return nil
}

type createRequest struct {
	Restaurant       flexString `json:"restaurant"`
	Customer         flexString `json:"customer"`
	Address          flexString `json:"address"`
	Courier          flexString `json:"courier"`
	Total            flexString `json:"total"`            // reais, e.g. 25 or "25.90"
	EstimatedMinutes flexString `json:"estimatedMinutes"` // optional, default 45
}

// parseCents converts a reais amount ("25", "25.9", "25,90") to cents.
func parseCents(s string) (int64, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", ".")
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > 1e12 {
		return 0, false
	}
	return int64(math.Round(f * 100)), true
}

func parseMinutes(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return domain.DefaultEstimate, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != math.Trunc(f) || math.Abs(f) > 1e6 {
		return 0, false
	}
	return int(f), true
}

func (a *api) createOrder(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if !a.decode(w, r, &req) {
		return
	}
	in := domain.NewOrder{
		Restaurant: string(req.Restaurant), Customer: string(req.Customer),
		Address: string(req.Address), Courier: string(req.Courier),
	}
	fields := map[string]string{}
	var ok bool
	if in.TotalCents, ok = parseCents(string(req.Total)); !ok {
		fields["total"] = "Informe o valor do pedido."
	}
	if in.EstimatedMinutes, ok = parseMinutes(string(req.EstimatedMinutes)); !ok {
		fields["estimatedMinutes"] = "Informe o tempo estimado em minutos inteiros."
	}
	err := in.Validate()
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		for k, v := range ve.Fields {
			if _, set := fields[k]; !set {
				fields[k] = v
			}
		}
	} else if err != nil {
		a.internalError(w, r, err)
		return
	}
	if len(fields) > 0 {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: msgInvalidOrder, Fields: fields})
		return
	}
	o, err := a.store.CreateOrder(r.Context(), in, a.now())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	a.hub.Publish("order", o)
	writeJSON(w, http.StatusCreated, toOrderJSON(o, a.now()))
}

type statusRequest struct {
	Status string `json:"status"`
	Note   string `json:"note"`
	// From is optional optimistic concurrency: the status the client saw.
	From string `json:"from"`
}

func (a *api) changeStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, msgOrderNotFound)
		return
	}
	var req statusRequest
	if !a.decode(w, r, &req) {
		return
	}
	to := domain.Status(strings.TrimSpace(req.Status))
	from := domain.Status(strings.TrimSpace(req.From))
	if !to.Valid() || (from != "" && !from.Valid()) {
		writeError(w, http.StatusBadRequest, msgInvalidStatus)
		return
	}
	note := domain.CleanText(req.Note)
	if len([]rune(note)) > domain.MaxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "Observação muito longa.", Fields: map[string]string{"note": "Use no máximo 200 caracteres."}})
		return
	}
	o, err := a.store.ChangeStatus(r.Context(), id, from, to, note, a.now())
	var te *domain.TransitionError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, msgOrderNotFound)
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, msgConflict)
	case errors.As(err, &te):
		writeError(w, http.StatusConflict, "Não é possível mudar de “"+statusLabels[te.From]+"” para “"+statusLabels[te.To]+"”. Confira o fluxo do pedido.")
	case err != nil:
		a.internalError(w, r, err)
	default:
		a.hub.Publish("order", o)
		writeJSON(w, http.StatusOK, toOrderJSON(o, a.now()))
	}
}

func (a *api) trackOrder(w http.ResponseWriter, r *http.Request) {
	o, ok := a.lookupCode(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toTrackJSON(o, a.now()))
}

func (a *api) lookupCode(w http.ResponseWriter, r *http.Request) (domain.Order, bool) {
	code := domain.NormalizeCode(chi.URLParam(r, "code"))
	if code == "" || len(code) > domain.MaxCodeLen {
		writeError(w, http.StatusNotFound, msgTrackNotFound)
		return domain.Order{}, false
	}
	o, err := a.store.GetOrderByCode(r.Context(), code)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, msgTrackNotFound)
		return o, false
	}
	if err != nil {
		a.internalError(w, r, err)
		return o, false
	}
	return o, true
}

// decode reads a size-limited JSON body; it writes the error response itself.
//
// Requiring application/json also blocks cross-site HTML form posts (CSRF):
// browsers cannot send that content type cross-origin without a CORS preflight,
// which this server never approves.
func (a *api) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, msgContentType)
		return false
	}
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(body)
	err := dec.Decode(dst)
	if err == nil {
		// Reject trailing garbage such as two concatenated objects.
		if _, extra := dec.Token(); extra != io.EOF {
			err = errors.New("trailing data")
		}
	}
	if err == nil {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, msgBodyTooLarge)
	} else {
		writeError(w, http.StatusBadRequest, msgBadBody)
	}
	return false
}
