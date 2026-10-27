package domain

import (
	"crypto/rand"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Validation limits shared by the API and the store.
const (
	MinTextLen       = 3
	MaxNameLen       = 70
	MaxAddressLen    = 140
	MaxNoteLen       = 200
	MinTotalCents    = 100
	MaxTotalCents    = 500_000
	MinEstimate      = 10
	MaxEstimate      = 180
	DefaultEstimate  = 45
	trackingAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ" // no 0/1/I/L/O/U: easy to read aloud
	trackingPrefix   = "EV-"
	trackingLen      = 8
	MaxCodeLen       = 16
)

// Order is a delivery order with its event history.
type Order struct {
	ID               int64
	Code             string
	Restaurant       string
	Customer         string
	Courier          string
	Address          string
	TotalCents       int64
	EstimatedMinutes int
	Status           Status
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Events           []Event
}

// Event is one entry of an order's status history.
type Event struct {
	Status    Status
	Note      string
	CreatedAt time.Time
}

// EstimatedDeliveryAt is the naive ETA: creation time plus the estimate, in UTC.
func (o Order) EstimatedDeliveryAt() time.Time {
	return o.CreatedAt.Add(time.Duration(o.EstimatedMinutes) * time.Minute).UTC()
}

// RemainingMinutes is the whole number of minutes left until the ETA, never negative.
func (o Order) RemainingMinutes(now time.Time) int {
	elapsed := int(now.Sub(o.CreatedAt) / time.Minute)
	if elapsed < 0 {
		elapsed = 0
	}
	return max(0, o.EstimatedMinutes-elapsed)
}

// NewOrder is the validated input to create an order.
type NewOrder struct {
	Restaurant       string
	Customer         string
	Address          string
	Courier          string
	TotalCents       int64
	EstimatedMinutes int
}

// ValidationError carries user-facing (pt-BR) messages per field.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "invalid order fields: " + strings.Join(keys, ", ")
}

// CleanText trims and collapses internal whitespace, as the UI expects.
func CleanText(s string) string { return strings.Join(strings.Fields(s), " ") }

// Normalize cleans text fields in place.
func (n *NewOrder) Normalize() {
	n.Restaurant = CleanText(n.Restaurant)
	n.Customer = CleanText(n.Customer)
	n.Address = CleanText(n.Address)
	n.Courier = CleanText(n.Courier)
}

// Validate normalizes and checks the input, returning *ValidationError on failure.
func (n *NewOrder) Validate() error {
	n.Normalize()
	fields := map[string]string{}
	text := func(name, v string, maxLen int, required bool) {
		l := utf8.RuneCountInString(v)
		switch {
		case required && l < MinTextLen:
			fields[name] = fmt.Sprintf("Informe ao menos %d caracteres.", MinTextLen)
		case l > maxLen:
			fields[name] = fmt.Sprintf("Use no máximo %d caracteres.", maxLen)
		}
	}
	text("restaurant", n.Restaurant, MaxNameLen, true)
	text("customer", n.Customer, MaxNameLen, true)
	text("address", n.Address, MaxAddressLen, true)
	text("courier", n.Courier, MaxNameLen, false)
	if n.TotalCents < MinTotalCents || n.TotalCents > MaxTotalCents {
		fields["total"] = "Informe um valor entre R$ 1,00 e R$ 5.000,00."
	}
	if n.EstimatedMinutes < MinEstimate || n.EstimatedMinutes > MaxEstimate {
		fields["estimatedMinutes"] = fmt.Sprintf("Use entre %d e %d minutos.", MinEstimate, MaxEstimate)
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// NewTrackingCode returns a random, non-sequential public code such as EV-7K3M-Q9XA
// (8 symbols from a 30-symbol alphabet, ~39 bits of entropy).
func NewTrackingCode() (string, error) {
	buf := make([]byte, trackingLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("tracking code: %w", err)
	}
	// 256 % 30 != 0, so reject bytes >= 240 to avoid modulo bias.
	out := make([]byte, 0, trackingLen)
	for len(out) < trackingLen {
		for _, b := range buf {
			if b < 240 && len(out) < trackingLen {
				out = append(out, trackingAlphabet[int(b)%len(trackingAlphabet)])
			}
		}
		if len(out) < trackingLen {
			if _, err := rand.Read(buf); err != nil {
				return "", fmt.Errorf("tracking code: %w", err)
			}
		}
	}
	return trackingPrefix + string(out[:4]) + "-" + string(out[4:]), nil
}

// NormalizeCode canonicalizes user input for lookup. Codes are stored upper case.
func NormalizeCode(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}
