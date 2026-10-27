// Package domain holds the order model, its state machine and validation rules.
package domain

import (
	"errors"
	"fmt"
)

// Status is the lifecycle stage of an order.
type Status string

// Order statuses. Values are persisted and exposed in the API; do not rename.
const (
	StatusReceived  Status = "recebido"
	StatusPreparing Status = "preparando"
	StatusReady     Status = "pronto"
	StatusOnRoute   Status = "em_entrega"
	StatusDelivered Status = "entregue"
	StatusCanceled  Status = "cancelado"
)

// AllStatuses lists every status in flow order.
var AllStatuses = []Status{
	StatusReceived, StatusPreparing, StatusReady, StatusOnRoute, StatusDelivered, StatusCanceled,
}

// transitions is the single source of truth for the order flow.
var transitions = map[Status][]Status{
	StatusReceived:  {StatusPreparing, StatusCanceled},
	StatusPreparing: {StatusReady, StatusCanceled},
	StatusReady:     {StatusOnRoute, StatusCanceled},
	StatusOnRoute:   {StatusDelivered},
	StatusDelivered: {},
	StatusCanceled:  {},
}

// eventNotes are the default history notes recorded when entering a status.
var eventNotes = map[Status]string{
	StatusReceived:  "Pedido recebido e aguardando a cozinha",
	StatusPreparing: "Cozinha iniciou o preparo",
	StatusReady:     "Pedido pronto para retirada",
	StatusOnRoute:   "Entregador saiu para entrega",
	StatusDelivered: "Pedido entregue ao cliente",
	StatusCanceled:  "Pedido cancelado",
}

// ErrUnknownStatus is returned for values outside the defined statuses.
var ErrUnknownStatus = errors.New("unknown status")

// ErrInvalidTransition is matched (errors.Is) by every *TransitionError.
var ErrInvalidTransition = errors.New("invalid status transition")

// TransitionError describes a rejected move between two statuses.
type TransitionError struct {
	From Status
	To   Status
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("invalid status transition from %q to %q", e.From, e.To)
}

// Is makes errors.Is(err, ErrInvalidTransition) work.
func (e *TransitionError) Is(target error) bool { return target == ErrInvalidTransition }

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	_, ok := transitions[s]
	return ok
}

// Terminal reports whether no further transition is possible.
func (s Status) Terminal() bool {
	return s.Valid() && len(transitions[s]) == 0
}

// DefaultNote returns the history note used when no custom note is given.
func (s Status) DefaultNote() string { return eventNotes[s] }

// NextStatuses returns a copy of the allowed targets from s.
func (s Status) NextStatuses() []Status {
	return append([]Status(nil), transitions[s]...)
}

// CanTransition validates a move from -> to.
func CanTransition(from, to Status) error {
	if !from.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownStatus, from)
	}
	for _, s := range transitions[from] {
		if s == to {
			return nil
		}
	}
	return &TransitionError{From: from, To: to}
}

// Path returns the statuses an order passes through to reach target from
// StatusReceived along the happy path (used for demo seeding).
func Path(target Status) []Status {
	happy := []Status{StatusReceived, StatusPreparing, StatusReady, StatusOnRoute, StatusDelivered}
	for i, s := range happy {
		if s == target {
			return append([]Status(nil), happy[:i+1]...)
		}
	}
	if target == StatusCanceled {
		return []Status{StatusReceived, StatusCanceled}
	}
	return nil
}
