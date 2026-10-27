// Package events implements an in-process fan-out hub for Server-Sent Events.
//
// Publish never blocks: each subscriber has a bounded buffer and a subscriber
// that falls behind is disconnected (its channel is closed). The client then
// reconnects with Last-Event-ID and catches up from the replay ring, or is told
// to resync when the gap is no longer available.
package events

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
)

// ErrClosed is returned by Subscribe after Close.
var ErrClosed = errors.New("events: hub closed")

// ErrTooManySubscribers is returned when the subscriber limit is reached.
var ErrTooManySubscribers = errors.New("events: too many subscribers")

// Event is one published message. Payload is opaque to the hub.
type Event struct {
	ID      string
	Seq     uint64
	Type    string
	Payload any
}

// Options configures a Hub. Zero values pick defaults.
type Options struct {
	BufferSize     int // per-subscriber channel capacity (default 32)
	ReplaySize     int // events kept for Last-Event-ID replay (default 256)
	MaxSubscribers int // 0 means unlimited
}

// Hub fans events out to subscribers.
type Hub struct {
	mu      sync.Mutex
	epoch   string
	seq     uint64
	ring    []Event // circular buffer of the last ReplaySize events
	start   int
	subs    map[*Subscription]struct{}
	closed  bool
	bufSize int
	maxSubs int
}

// Subscription receives events on C until it is closed by the hub
// (lag, hub shutdown) or by Unsubscribe.
type Subscription struct {
	C      <-chan Event
	c      chan Event
	hub    *Hub
	lagged bool
	once   sync.Once
}

// NewHub creates a hub. Event IDs are "<epoch>-<seq>" so IDs from a previous
// process are recognized as foreign after a restart.
func NewHub(o Options) *Hub {
	if o.BufferSize <= 0 {
		o.BufferSize = 32
	}
	if o.ReplaySize <= 0 {
		o.ReplaySize = 256
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return &Hub{
		epoch:   hex.EncodeToString(b),
		ring:    make([]Event, 0, o.ReplaySize),
		subs:    map[*Subscription]struct{}{},
		bufSize: o.BufferSize,
		maxSubs: o.MaxSubscribers,
	}
}

// Publish records the event and delivers it to every subscriber without blocking.
func (h *Hub) Publish(typ string, payload any) Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	ev := Event{ID: h.idFor(h.seq), Seq: h.seq, Type: typ, Payload: payload}
	if h.closed {
		return ev
	}
	if len(h.ring) < cap(h.ring) {
		h.ring = append(h.ring, ev)
	} else {
		h.ring[h.start] = ev
		h.start = (h.start + 1) % len(h.ring)
	}
	for s := range h.subs {
		select {
		case s.c <- ev:
		default:
			// Slow consumer: drop it rather than block everyone else.
			s.lagged = true
			h.removeLocked(s)
		}
	}
	return ev
}

// Resume tells a new subscriber how to catch up.
type Resume struct {
	// Replay holds events published after the client's Last-Event-ID.
	Replay []Event
	// Resync is true when the given ID is unknown (another process epoch or
	// older than the replay window): the client must reload its full state.
	Resync bool
	// CurrentID is the latest event ID at subscription time; sending it to the
	// client lets a later reconnect resume from here.
	CurrentID string
}

// Subscribe registers a subscriber. lastEventID is the client's Last-Event-ID
// and may be empty for a fresh connection.
func (h *Hub) Subscribe(lastEventID string) (*Subscription, Resume, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, Resume{}, ErrClosed
	}
	if h.maxSubs > 0 && len(h.subs) >= h.maxSubs {
		return nil, Resume{}, ErrTooManySubscribers
	}
	res := Resume{CurrentID: h.idFor(h.seq)}
	if lastEventID != "" {
		res.Replay, res.Resync = h.since(lastEventID)
	}
	c := make(chan Event, h.bufSize)
	sub := &Subscription{C: c, c: c, hub: h}
	h.subs[sub] = struct{}{}
	return sub, res, nil
}

func (h *Hub) idFor(seq uint64) string { return h.epoch + "-" + strconv.FormatUint(seq, 10) }

func (h *Hub) since(id string) ([]Event, bool) {
	epoch, seqStr, ok := strings.Cut(id, "-")
	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if !ok || err != nil || epoch != h.epoch || seq > h.seq {
		return nil, true
	}
	if seq == h.seq {
		return nil, false
	}
	n := len(h.ring)
	if n == 0 {
		return nil, true
	}
	oldest := h.ring[h.start].Seq
	if seq+1 < oldest {
		return nil, true
	}
	out := make([]Event, 0, h.seq-seq)
	for i := range n {
		ev := h.ring[(h.start+i)%n]
		if ev.Seq > seq {
			out = append(out, ev)
		}
	}
	return out, false
}

// Unsubscribe removes the subscription; safe to call more than once.
func (s *Subscription) Unsubscribe() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.removeLocked(s)
}

// Lagged reports whether the hub dropped this subscriber for being too slow.
// Only meaningful after C is closed.
func (s *Subscription) Lagged() bool {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return s.lagged
}

func (h *Hub) removeLocked(s *Subscription) {
	if _, ok := h.subs[s]; !ok {
		return
	}
	delete(h.subs, s)
	s.once.Do(func() { close(s.c) })
}

// Len returns the number of active subscribers.
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Close disconnects every subscriber and rejects new ones. Used on shutdown so
// long-lived SSE handlers return and http.Server.Shutdown can complete.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for s := range h.subs {
		h.removeLocked(s)
	}
}
