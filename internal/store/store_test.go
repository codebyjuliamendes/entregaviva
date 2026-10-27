package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/codebyjuliamendes/entregaviva/internal/domain"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "test.sqlite")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func sample() domain.NewOrder {
	return domain.NewOrder{Restaurant: "Corner Cafe", Customer: "Guest Person", Address: "1 Main St", TotalCents: 2500, EstimatedMinutes: 60}
}

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func TestCreateAndGet(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	o, err := s.CreateOrder(ctx, sample(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if o.ID == 0 || o.Status != domain.StatusReceived || o.EstimatedMinutes != 60 || !o.CreatedAt.Equal(t0) {
		t.Fatalf("unexpected order %+v", o)
	}
	if len(o.Events) != 1 || o.Events[0].Status != domain.StatusReceived {
		t.Fatalf("events %+v", o.Events)
	}
	byCode, err := s.GetOrderByCode(ctx, " "+toLower(o.Code)+" ")
	if err != nil || byCode.ID != o.ID {
		t.Fatalf("by code: %v %+v", err, byCode)
	}
	if _, err := s.GetOrder(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := s.GetOrderByCode(ctx, "EV-NOPE-NOPE"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func TestCreateValidation(t *testing.T) {
	s, _ := openTest(t)
	in := sample()
	in.EstimatedMinutes = 5
	var ve *domain.ValidationError
	if _, err := s.CreateOrder(context.Background(), in, t0); !errors.As(err, &ve) {
		t.Fatalf("want validation error, got %v", err)
	}
}

func TestChangeStatus(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	o, _ := s.CreateOrder(ctx, sample(), t0)

	if _, err := s.ChangeStatus(ctx, o.ID, "", domain.StatusDelivered, "", t0); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("want invalid transition, got %v", err)
	}
	up, err := s.ChangeStatus(ctx, o.ID, domain.StatusReceived, domain.StatusPreparing, "  sem   cebola ", t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if up.Status != domain.StatusPreparing || !up.UpdatedAt.Equal(t0.Add(time.Minute)) || len(up.Events) != 2 || up.Events[1].Note != "sem cebola" {
		t.Fatalf("unexpected %+v", up)
	}
	// Stale screen still thinks the order is "recebido".
	if _, err := s.ChangeStatus(ctx, o.ID, domain.StatusReceived, domain.StatusPreparing, "", t0); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	if _, err := s.ChangeStatus(ctx, 4242, "", domain.StatusPreparing, "", t0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	up, _ = s.ChangeStatus(ctx, o.ID, "", domain.StatusReady, "", t0)
	if up.Events[2].Note != domain.StatusReady.DefaultNote() {
		t.Fatalf("default note: %q", up.Events[2].Note)
	}
}

func TestConcurrentTransitionsOnlyOneWins(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	o, _ := s.CreateOrder(ctx, sample(), t0)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ChangeStatus(ctx, o.ID, domain.StatusReceived, domain.StatusPreparing, "", t0); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("wins = %d", wins)
	}
	got, _ := s.GetOrder(ctx, o.ID)
	if len(got.Events) != 2 {
		t.Fatalf("events = %d", len(got.Events))
	}
}

func TestListOrdersOrderingAndClosedLimit(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	mk := func(to ...domain.Status) domain.Order {
		o, err := s.CreateOrder(ctx, sample(), t0)
		if err != nil {
			t.Fatal(err)
		}
		for i, st := range to {
			if o, err = s.ChangeStatus(ctx, o.ID, "", st, "", t0.Add(time.Duration(o.ID*10+int64(i))*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		return o
	}
	ready := mk(domain.StatusPreparing, domain.StatusReady)
	received := mk()
	for range ClosedListLimit + 3 {
		mk(domain.StatusCanceled)
	}
	list, err := s.ListOrders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2+ClosedListLimit {
		t.Fatalf("len = %d", len(list))
	}
	if list[0].ID != received.ID || list[1].ID != ready.ID {
		t.Fatalf("active order wrong: %d %d", list[0].ID, list[1].ID)
	}
	if len(list[1].Events) != 3 {
		t.Fatalf("events not attached: %+v", list[1].Events)
	}
	for i := 3; i < len(list); i++ {
		if list[i-1].UpdatedAt.Before(list[i].UpdatedAt) {
			t.Fatal("closed orders must be newest first")
		}
	}
}

func TestSeedDemoIsIdempotent(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	ok, err := s.SeedDemo(ctx, t0)
	if err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	ok, err = s.SeedDemo(ctx, t0)
	if err != nil || ok {
		t.Fatalf("second seed: %v %v", ok, err)
	}
	list, _ := s.ListOrders(ctx)
	if len(list) != 4 {
		t.Fatalf("len %d", len(list))
	}
	for _, o := range list {
		path := domain.Path(o.Status)
		if len(o.Events) != len(path) {
			t.Fatalf("%s: %d events for %v", o.Code, len(o.Events), path)
		}
		for i, ev := range o.Events {
			if ev.Status != path[i] {
				t.Fatalf("%s: event %d has status %s, want %s", o.Code, i, ev.Status, path[i])
			}
		}
	}
}

func TestMigratesLegacyNodeDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `CREATE TABLE orders(id INTEGER PRIMARY KEY,code TEXT NOT NULL UNIQUE,restaurant TEXT NOT NULL,customer TEXT NOT NULL,courier TEXT NOT NULL DEFAULT '',address TEXT NOT NULL,total_cents INTEGER NOT NULL,estimated_minutes INTEGER NOT NULL DEFAULT 45,status TEXT NOT NULL CHECK(status IN ('recebido','preparando','pronto','em_entrega','entregue','cancelado')),created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE order_events(id INTEGER PRIMARY KEY,order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,status TEXT NOT NULL,note TEXT NOT NULL,created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
INSERT INTO orders(code,restaurant,customer,address,total_cents,status,created_at,updated_at) VALUES('fv-4821','Casa','Marina','Rua A, 1',5490,'recebido','2026-01-02 10:00:00','2026-01-02 10:00:00');
INSERT INTO order_events(order_id,status,note,created_at) VALUES(1,'recebido','Pedido recebido','2026-01-02 10:00:00');`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	o, err := s.GetOrderByCode(context.Background(), "FV-4821")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	if !o.CreatedAt.Equal(want) || !o.Events[0].CreatedAt.Equal(want) {
		t.Fatalf("timestamps not migrated: %v", o.CreatedAt)
	}
	// Re-opening must not re-apply migrations.
	_ = s.Close()
	s2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()
}

func TestPing(t *testing.T) {
	s, _ := openTest(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
