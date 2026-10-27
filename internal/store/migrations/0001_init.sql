-- Baseline schema. IF NOT EXISTS keeps databases created by the previous
-- Node.js backend usable: their tables already match this shape.
CREATE TABLE IF NOT EXISTS orders (
  id INTEGER PRIMARY KEY,
  code TEXT NOT NULL UNIQUE,
  restaurant TEXT NOT NULL,
  customer TEXT NOT NULL,
  courier TEXT NOT NULL DEFAULT '',
  address TEXT NOT NULL,
  total_cents INTEGER NOT NULL,
  estimated_minutes INTEGER NOT NULL DEFAULT 45,
  status TEXT NOT NULL CHECK (status IN ('recebido','preparando','pronto','em_entrega','entregue','cancelado')),
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS order_events (
  id INTEGER PRIMARY KEY,
  order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  status TEXT NOT NULL,
  note TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
