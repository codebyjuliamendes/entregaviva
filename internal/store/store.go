// Package store persists orders in SQLite (modernc.org/sqlite, CGO-free).
//
// Two pools are used: a single-connection writer with BEGIN IMMEDIATE
// transactions (SQLite allows one writer) and a read-only pool for queries,
// which WAL lets run concurrently with writes.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/codebyjuliamendes/entregaviva/internal/domain"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

var (
	// ErrNotFound is returned when an order does not exist.
	ErrNotFound = errors.New("order not found")
	// ErrConflict is returned when the order changed concurrently.
	ErrConflict = errors.New("order changed concurrently")
)

// ClosedListLimit caps how many delivered/canceled orders ListOrders returns.
const ClosedListLimit = 50

const timeLayout = time.RFC3339

const orderColumns = `id, code, restaurant, customer, courier, address, total_cents, estimated_minutes, status, created_at, updated_at`

const statusRank = `CASE status WHEN 'recebido' THEN 1 WHEN 'preparando' THEN 2 WHEN 'pronto' THEN 3 WHEN 'em_entrega' THEN 4 ELSE 5 END`

// visibleCTE selects every active order plus the most recently closed ones.
const visibleCTE = `WITH visible(id) AS (
	SELECT id FROM orders WHERE status NOT IN ('entregue','cancelado')
	UNION ALL
	SELECT id FROM (SELECT id FROM orders WHERE status IN ('entregue','cancelado') ORDER BY updated_at DESC, id DESC LIMIT ?)
)`

// Store is safe for concurrent use.
type Store struct {
	w, r *sql.DB

	listOrders, listEvents, getOrder, getByCode, orderEvents *sql.Stmt
	insertOrder, insertEvent, selectStatus, updateStatus     *sql.Stmt
	countOrders                                              *sql.Stmt
}

// Open creates the database file if needed, applies migrations and prepares statements.
func Open(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetConnMaxIdleTime(0)
	if err := w.PingContext(ctx); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := migrate(ctx, w); err != nil {
		_ = w.Close()
		return nil, err
	}
	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)
	s := &Store{w: w, r: r}
	if err := s.prepare(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	return "file:" + path + "?" + q.Encode()
}

func (s *Store) prepare(ctx context.Context) error {
	type p struct {
		db   *sql.DB
		dst  **sql.Stmt
		text string
	}
	list := []p{
		{s.r, &s.listOrders, visibleCTE + ` SELECT ` + orderColumns + ` FROM orders WHERE id IN (SELECT id FROM visible)
			ORDER BY ` + statusRank + `, CASE WHEN status IN ('entregue','cancelado') THEN updated_at END DESC, id DESC`},
		{s.r, &s.listEvents, visibleCTE + ` SELECT order_id, status, note, created_at FROM order_events
			WHERE order_id IN (SELECT id FROM visible) ORDER BY order_id, id`},
		{s.r, &s.getOrder, `SELECT ` + orderColumns + ` FROM orders WHERE id = ?`},
		{s.r, &s.getByCode, `SELECT ` + orderColumns + ` FROM orders WHERE code = ?`},
		{s.r, &s.orderEvents, `SELECT status, note, created_at FROM order_events WHERE order_id = ? ORDER BY id`},
		{s.w, &s.countOrders, `SELECT COUNT(*) FROM orders`},
		{s.w, &s.insertOrder, `INSERT INTO orders(code, restaurant, customer, courier, address, total_cents, estimated_minutes, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`},
		{s.w, &s.insertEvent, `INSERT INTO order_events(order_id, status, note, created_at) VALUES (?, ?, ?, ?)`},
		{s.w, &s.selectStatus, `SELECT status FROM orders WHERE id = ?`},
		{s.w, &s.updateStatus, `UPDATE orders SET status = ?, updated_at = ? WHERE id = ? AND status = ?`},
	}
	for _, it := range list {
		st, err := it.db.PrepareContext(ctx, it.text)
		if err != nil {
			return fmt.Errorf("prepare %q: %w", firstLine(it.text), err)
		}
		*it.dst = st
	}
	return nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

// Close releases statements and both pools.
func (s *Store) Close() error {
	for _, st := range []*sql.Stmt{s.listOrders, s.listEvents, s.getOrder, s.getByCode, s.orderEvents,
		s.insertOrder, s.insertEvent, s.selectStatus, s.updateStatus, s.countOrders} {
		if st != nil {
			_ = st.Close()
		}
	}
	var errs []error
	if s.r != nil {
		errs = append(errs, s.r.Close())
	}
	if s.w != nil {
		errs = append(errs, s.w.Close())
	}
	return errors.Join(errs...)
}

// Ping checks that the database answers.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	return s.r.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
}

type scanner interface{ Scan(...any) error }

func scanOrder(row scanner) (domain.Order, error) {
	var o domain.Order
	var status, created, updated string
	if err := row.Scan(&o.ID, &o.Code, &o.Restaurant, &o.Customer, &o.Courier, &o.Address,
		&o.TotalCents, &o.EstimatedMinutes, &status, &created, &updated); err != nil {
		return o, err
	}
	o.Status = domain.Status(status)
	var err error
	if o.CreatedAt, err = parseTime(created); err != nil {
		return o, err
	}
	if o.UpdatedAt, err = parseTime(updated); err != nil {
		return o, err
	}
	return o, nil
}

// ListOrders returns active orders (flow order, newest first) followed by the
// most recently closed ones, each with its event history.
func (s *Store) ListOrders(ctx context.Context) ([]domain.Order, error) {
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	orders := []domain.Order{}
	index := map[int64]int{}
	err = queryEach(ctx, tx.StmtContext(ctx, s.listOrders), func(rows *sql.Rows) error {
		o, err := scanOrder(rows)
		if err != nil {
			return err
		}
		o.Events = []domain.Event{}
		index[o.ID] = len(orders)
		orders = append(orders, o)
		return nil
	}, ClosedListLimit)
	if err != nil {
		return nil, err
	}
	err = queryEach(ctx, tx.StmtContext(ctx, s.listEvents), func(rows *sql.Rows) error {
		var id int64
		ev, err := scanEvent(rows, &id)
		if err != nil {
			return err
		}
		if i, ok := index[id]; ok {
			orders[i].Events = append(orders[i].Events, ev)
		}
		return nil
	}, ClosedListLimit)
	if err != nil {
		return nil, err
	}
	return orders, tx.Commit()
}

// queryEach runs stmt and calls fn for every row, always closing the rows.
func queryEach(ctx context.Context, stmt *sql.Stmt, fn func(*sql.Rows) error, args ...any) error {
	rows, err := stmt.QueryContext(ctx, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func scanEvent(row scanner, orderID *int64) (domain.Event, error) {
	var ev domain.Event
	var status, created string
	dst := []any{&status, &ev.Note, &created}
	if orderID != nil {
		dst = append([]any{orderID}, dst...)
	}
	if err := row.Scan(dst...); err != nil {
		return ev, err
	}
	ev.Status = domain.Status(status)
	t, err := parseTime(created)
	ev.CreatedAt = t
	return ev, err
}

// GetOrder loads one order by id.
func (s *Store) GetOrder(ctx context.Context, id int64) (domain.Order, error) {
	return s.loadOrder(ctx, s.getOrder, id)
}

// GetOrderByCode loads one order by its public tracking code (case-insensitive).
func (s *Store) GetOrderByCode(ctx context.Context, code string) (domain.Order, error) {
	return s.loadOrder(ctx, s.getByCode, domain.NormalizeCode(code))
}

func (s *Store) loadOrder(ctx context.Context, stmt *sql.Stmt, key any) (domain.Order, error) {
	tx, err := s.r.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domain.Order{}, err
	}
	defer func() { _ = tx.Rollback() }()
	o, err := scanOrder(tx.StmtContext(ctx, stmt).QueryRowContext(ctx, key))
	if errors.Is(err, sql.ErrNoRows) {
		return o, ErrNotFound
	}
	if err != nil {
		return o, err
	}
	o.Events = []domain.Event{}
	err = queryEach(ctx, tx.StmtContext(ctx, s.orderEvents), func(rows *sql.Rows) error {
		ev, err := scanEvent(rows, nil)
		if err != nil {
			return err
		}
		o.Events = append(o.Events, ev)
		return nil
	}, o.ID)
	if err != nil {
		return o, err
	}
	return o, tx.Commit()
}

// CreateOrder validates and inserts a new order in status "recebido".
func (s *Store) CreateOrder(ctx context.Context, in domain.NewOrder, now time.Time) (domain.Order, error) {
	if err := in.Validate(); err != nil {
		return domain.Order{}, err
	}
	var id int64
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		id, err = s.insertWithEvents(ctx, tx, in, []domain.Status{domain.StatusReceived}, now, []time.Time{now})
		return err
	})
	if err != nil {
		return domain.Order{}, err
	}
	return s.GetOrder(ctx, id)
}

// insertWithEvents inserts an order whose history follows path; the order ends
// in the last status of path. Codes are random, so retry on the rare collision.
func (s *Store) insertWithEvents(ctx context.Context, tx *sql.Tx, in domain.NewOrder, path []domain.Status, created time.Time, at []time.Time) (int64, error) {
	final := path[len(path)-1]
	ins := tx.StmtContext(ctx, s.insertOrder)
	var res sql.Result
	for attempt := 0; ; attempt++ {
		code, err := domain.NewTrackingCode()
		if err != nil {
			return 0, err
		}
		res, err = ins.ExecContext(ctx, code, in.Restaurant, in.Customer, in.Courier, in.Address,
			in.TotalCents, in.EstimatedMinutes, string(final), formatTime(created), formatTime(at[len(at)-1]))
		if err == nil {
			break
		}
		if !isUniqueViolation(err) || attempt >= 4 {
			return 0, fmt.Errorf("insert order: %w", err)
		}
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	insEv := tx.StmtContext(ctx, s.insertEvent)
	for i, st := range path {
		if _, err := insEv.ExecContext(ctx, id, string(st), st.DefaultNote(), formatTime(at[i])); err != nil {
			return 0, fmt.Errorf("insert event: %w", err)
		}
	}
	return id, nil
}

// ChangeStatus applies a validated transition and records it in the history.
// When expected is non-empty and differs from the stored status, ErrConflict
// is returned so stale screens do not act on outdated data.
func (s *Store) ChangeStatus(ctx context.Context, id int64, expected, to domain.Status, note string, now time.Time) (domain.Order, error) {
	note = domain.CleanText(note)
	if note == "" {
		note = to.DefaultNote()
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var current string
		err := tx.StmtContext(ctx, s.selectStatus).QueryRowContext(ctx, id).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		from := domain.Status(current)
		if expected != "" && expected != from {
			return ErrConflict
		}
		if err := domain.CanTransition(from, to); err != nil {
			return err
		}
		ts := formatTime(now)
		res, err := tx.StmtContext(ctx, s.updateStatus).ExecContext(ctx, string(to), ts, id, string(from))
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrConflict
		}
		_, err = tx.StmtContext(ctx, s.insertEvent).ExecContext(ctx, id, string(to), note, ts)
		return err
	})
	if err != nil {
		return domain.Order{}, err
	}
	return s.GetOrder(ctx, id)
}

func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// SeedDemo inserts fictitious orders when the database is empty.
// It reports whether anything was inserted.
func (s *Store) SeedDemo(ctx context.Context, now time.Time) (bool, error) {
	demo := []struct {
		in     domain.NewOrder
		status domain.Status
		ago    time.Duration
	}{
		{domain.NewOrder{Restaurant: "Casa do Bento", Customer: "Marina Lopes", Courier: "Rafa", Address: "Rua das Flores, 245", TotalCents: 5490, EstimatedMinutes: 45}, domain.StatusPreparing, 12 * time.Minute},
		{domain.NewOrder{Restaurant: "Pizza da Vila", Customer: "Caio Martins", Address: "Av. Central, 81", TotalCents: 8290, EstimatedMinutes: 50}, domain.StatusReceived, 3 * time.Minute},
		{domain.NewOrder{Restaurant: "Tempero de Casa", Customer: "João Pedro", Courier: "Luiza", Address: "Rua Aurora, 32", TotalCents: 4290, EstimatedMinutes: 40}, domain.StatusOnRoute, 31 * time.Minute},
		{domain.NewOrder{Restaurant: "Sushi Nori", Customer: "Bia Fernandes", Address: "Alameda Santos, 17", TotalCents: 6790, EstimatedMinutes: 45}, domain.StatusReady, 22 * time.Minute},
	}
	inserted := false
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.StmtContext(ctx, s.countOrders).QueryRowContext(ctx).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		for _, d := range demo {
			created := now.Add(-d.ago)
			path := domain.Path(d.status)
			at := make([]time.Time, len(path))
			for i := range path {
				// Spread the history evenly between creation and now.
				at[i] = created.Add(d.ago * time.Duration(i) / time.Duration(len(path)))
			}
			if _, err := s.insertWithEvents(ctx, tx, d.in, path, created, at); err != nil {
				return err
			}
		}
		inserted = true
		return nil
	})
	return inserted, err
}

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// parseTime accepts RFC 3339 and the legacy SQLite CURRENT_TIMESTAMP format.
func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.DateTime, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", s, err)
	}
	return t.UTC(), nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
