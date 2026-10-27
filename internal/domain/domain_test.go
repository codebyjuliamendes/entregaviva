package domain

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestTransitionTable(t *testing.T) {
	allowed := map[[2]Status]bool{
		{StatusReceived, StatusPreparing}: true,
		{StatusReceived, StatusCanceled}:  true,
		{StatusPreparing, StatusReady}:    true,
		{StatusPreparing, StatusCanceled}: true,
		{StatusReady, StatusOnRoute}:      true,
		{StatusReady, StatusCanceled}:     true,
		{StatusOnRoute, StatusDelivered}:  true,
	}
	for _, from := range AllStatuses {
		for _, to := range append(AllStatuses, Status("inexistente"), Status("")) {
			err := CanTransition(from, to)
			want := allowed[[2]Status{from, to}]
			if want && err != nil {
				t.Errorf("%s -> %s: unexpected error %v", from, to, err)
			}
			if !want {
				var te *TransitionError
				if !errors.As(err, &te) || !errors.Is(err, ErrInvalidTransition) {
					t.Errorf("%s -> %s: want TransitionError, got %v", from, to, err)
				}
			}
		}
	}
}

func TestUnknownFromStatus(t *testing.T) {
	if err := CanTransition("x", StatusReady); !errors.Is(err, ErrUnknownStatus) {
		t.Fatalf("got %v", err)
	}
}

func TestTerminalAndNotes(t *testing.T) {
	for _, s := range AllStatuses {
		if s.DefaultNote() == "" {
			t.Errorf("%s has no default note", s)
		}
		if got, want := s.Terminal(), s == StatusDelivered || s == StatusCanceled; got != want {
			t.Errorf("%s terminal=%v", s, got)
		}
	}
	if Status("x").Valid() {
		t.Error("unknown status reported valid")
	}
}

func TestTransitionErrorMessage(t *testing.T) {
	err := CanTransition(StatusReceived, StatusDelivered)
	if !strings.Contains(err.Error(), `from "recebido" to "entregue"`) {
		t.Fatalf("message: %q", err)
	}
}

func TestPathFollowsTransitions(t *testing.T) {
	for _, s := range AllStatuses {
		p := Path(s)
		if len(p) == 0 || p[0] != StatusReceived || p[len(p)-1] != s {
			t.Fatalf("bad path for %s: %v", s, p)
		}
		for i := 1; i < len(p); i++ {
			if err := CanTransition(p[i-1], p[i]); err != nil {
				t.Fatalf("path %v breaks the flow: %v", p, err)
			}
		}
	}
}

func TestValidate(t *testing.T) {
	ok := func() NewOrder {
		return NewOrder{Restaurant: "  Corner   Cafe ", Customer: "Guest Person", Address: "1 Main St", TotalCents: 2500, EstimatedMinutes: 60}
	}
	n := ok()
	if err := n.Validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if n.Restaurant != "Corner Cafe" {
		t.Fatalf("not normalized: %q", n.Restaurant)
	}
	cases := map[string]func(*NewOrder){
		"restaurant":       func(n *NewOrder) { n.Restaurant = " a " },
		"customer":         func(n *NewOrder) { n.Customer = strings.Repeat("á", MaxNameLen+1) },
		"address":          func(n *NewOrder) { n.Address = "" },
		"courier":          func(n *NewOrder) { n.Courier = strings.Repeat("x", MaxNameLen+1) },
		"total":            func(n *NewOrder) { n.TotalCents = 99 },
		"estimatedMinutes": func(n *NewOrder) { n.EstimatedMinutes = 5 },
	}
	for field, mutate := range cases {
		n := ok()
		mutate(&n)
		var ve *ValidationError
		if err := n.Validate(); !errors.As(err, &ve) || ve.Fields[field] == "" {
			t.Errorf("%s: want field error, got %v", field, err)
		}
	}
	// Multibyte names count runes, not bytes.
	n = ok()
	n.Customer = strings.Repeat("ç", MaxNameLen)
	if err := n.Validate(); err != nil {
		t.Fatalf("rune count: %v", err)
	}
}

func TestTrackingCode(t *testing.T) {
	re := regexp.MustCompile(`^EV-[2-9A-HJKMNP-TV-Z]{4}-[2-9A-HJKMNP-TV-Z]{4}$`)
	seen := map[string]bool{}
	for range 2000 {
		c, err := NewTrackingCode()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(c) || len(c) > MaxCodeLen {
			t.Fatalf("bad code %q", c)
		}
		if seen[c] {
			t.Fatalf("duplicate code %q", c)
		}
		seen[c] = true
	}
	if NormalizeCode("  ev-abcd-efgh ") != "EV-ABCD-EFGH" {
		t.Fatal("normalize")
	}
}

func TestRemainingMinutesAndETA(t *testing.T) {
	created := time.Date(2026, 1, 2, 10, 0, 0, 0, time.FixedZone("BRT", -3*3600))
	o := Order{CreatedAt: created, EstimatedMinutes: 45}
	if got := o.RemainingMinutes(created.Add(10*time.Minute + 30*time.Second)); got != 35 {
		t.Fatalf("remaining = %d", got)
	}
	if got := o.RemainingMinutes(created.Add(2 * time.Hour)); got != 0 {
		t.Fatalf("overdue remaining = %d", got)
	}
	if got := o.RemainingMinutes(created.Add(-time.Hour)); got != 45 {
		t.Fatalf("clock skew remaining = %d", got)
	}
	eta := o.EstimatedDeliveryAt()
	if eta.Location() != time.UTC || !eta.Equal(created.Add(45*time.Minute)) {
		t.Fatalf("eta %v", eta)
	}
}
