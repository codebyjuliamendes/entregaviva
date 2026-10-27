package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/codebyjuliamendes/entregaviva/internal/domain"
	"github.com/codebyjuliamendes/entregaviva/internal/events"
)

// sseWriteTimeout bounds each write so a stalled peer cannot pin a goroutine.
// The deadline is renewed before every write, so idle periods longer than it
// are fine. A variable so tests can shorten it.
var sseWriteTimeout = 10 * time.Second

// panelStream streams full order snapshots to the operations panel.
func (a *api) panelStream(w http.ResponseWriter, r *http.Request) {
	a.stream(w, r, func(o domain.Order, now time.Time) (any, bool) {
		return toOrderJSON(o, now), true
	})
}

// trackStream streams public snapshots of a single order.
func (a *api) trackStream(w http.ResponseWriter, r *http.Request) {
	tracked, ok := a.lookupCode(w, r)
	if !ok {
		return
	}
	a.stream(w, r, func(o domain.Order, now time.Time) (any, bool) {
		if o.ID != tracked.ID {
			return nil, false
		}
		return toTrackJSON(o, now), true
	})
}

// render maps a published order to the payload for this client, or false to skip it.
type render func(o domain.Order, now time.Time) (any, bool)

// stream runs one SSE connection until the client leaves, the hub drops it
// for lagging, or the server shuts down.
//
// Protocol: "connected" (carrying the current event id) is sent first; then
// "resync" if the client's Last-Event-ID could not be honoured (reload state);
// then missed "order" events, then live ones. Comments act as heartbeat.
func (a *api) stream(w http.ResponseWriter, r *http.Request, fn render) {
	rc := http.NewResponseController(w)
	sub, res, err := a.hub.Subscribe(r.Header.Get("Last-Event-ID"))
	if err != nil {
		w.Header().Set("Retry-After", "5")
		if errors.Is(err, events.ErrTooManySubscribers) {
			writeError(w, http.StatusServiceUnavailable, msgStreamBusy)
		} else {
			writeError(w, http.StatusServiceUnavailable, msgUnavailable)
		}
		return
	}
	defer sub.Unsubscribe()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	var buf bytes.Buffer
	write := func() bool {
		if err := rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		if _, err := w.Write(buf.Bytes()); err != nil {
			return false
		}
		buf.Reset()
		return rc.Flush() == nil
	}
	event := func(id, typ string, payload any) {
		data, err := json.Marshal(payload)
		if err != nil {
			a.log.ErrorContext(r.Context(), "sse marshal", "request_id", RequestID(r.Context()), "err", err)
			return
		}
		if id != "" {
			buf.WriteString("id: " + id + "\n")
		}
		buf.WriteString("event: " + typ + "\ndata: ")
		buf.Write(data)
		buf.WriteString("\n\n")
	}
	emit := func(ev events.Event) {
		o, ok := ev.Payload.(domain.Order)
		if !ok {
			return
		}
		if payload, keep := fn(o, a.now()); keep {
			event(ev.ID, ev.Type, payload)
		} else {
			// Still advance the client's Last-Event-ID so a reconnect does not
			// replay events it already skipped.
			buf.WriteString("id: " + ev.ID + "\n\n")
		}
	}

	buf.WriteString("retry: " + strconv.Itoa(streamRetryMillis) + "\n")
	event(res.CurrentID, "connected", struct{}{})
	if res.Resync {
		event("", "resync", struct{}{})
	}
	for _, ev := range res.Replay {
		emit(ev)
	}
	if !write() {
		return
	}

	heartbeat := time.NewTicker(a.cfg.SSEHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			buf.WriteString(": keepalive\n\n")
		case ev, ok := <-sub.C:
			if !ok {
				return // lagged or shutting down; the client reconnects
			}
			emit(ev)
		}
		if !write() {
			return
		}
	}
}
