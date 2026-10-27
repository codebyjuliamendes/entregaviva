-- Legacy rows used SQLite CURRENT_TIMESTAMP ("YYYY-MM-DD HH:MM:SS", UTC).
-- Normalize them to RFC 3339 UTC, the format written from now on.
UPDATE orders SET created_at = replace(created_at, ' ', 'T') || 'Z' WHERE created_at NOT LIKE '%Z';
UPDATE orders SET updated_at = replace(updated_at, ' ', 'T') || 'Z' WHERE updated_at NOT LIKE '%Z';
UPDATE order_events SET created_at = replace(created_at, ' ', 'T') || 'Z' WHERE created_at NOT LIKE '%Z';

-- Lookups are case-insensitive by storing codes upper case.
UPDATE orders SET code = upper(code) WHERE code <> upper(code);

CREATE INDEX IF NOT EXISTS idx_order_events_order ON order_events(order_id, id);
CREATE INDEX IF NOT EXISTS idx_orders_status_updated ON orders(status, updated_at);
