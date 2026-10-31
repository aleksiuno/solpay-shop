BEGIN;

CREATE TABLE products (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    slug           text        NOT NULL UNIQUE,                         -- format validated in Go
    title          text        NOT NULL,
    description    text        NOT NULL DEFAULT '',
    price_lamports bigint      NOT NULL CHECK (price_lamports BETWEEN 1 AND 1000000000000), -- ≤ 1000 SOL
    file_name      text        NOT NULL CHECK (file_name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$'), -- bare name in FILES_DIR
    active         boolean     NOT NULL DEFAULT true,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()            -- every UPDATE sets it; no trigger
);

CREATE TABLE orders (
    id                uuid        PRIMARY KEY,                          -- random v4 from Go (uuid.NewV4); the id is the bearer secret for /orders/{id}
    product_id        uuid        NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    quantity          integer     NOT NULL CHECK (quantity BETWEEN 1 AND 10),
    amount_lamports   bigint      NOT NULL CHECK (amount_lamports > 0), -- snapshot: price × quantity
    reference         text        NOT NULL UNIQUE,                      -- base58 of 32 random bytes
    status            text        NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending','underpaid','paid','fulfilled','expired','refunded')),
    received_lamports bigint      NOT NULL DEFAULT 0 CHECK (received_lamports >= 0), -- sum of credited ledger rows
    expires_at        timestamptz NOT NULL,                             -- app clock; never compared with now() in SQL
    next_poll_at      timestamptz,                                      -- next watcher poll; NULL once the order needs no more polling
    paid_at           timestamptz,
    fulfilled_at      timestamptz,
    refunded_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()          -- every UPDATE sets it; no trigger
);
CREATE INDEX orders_watch_idx      ON orders (next_poll_at)
    WHERE next_poll_at IS NOT NULL AND status IN ('pending','underpaid','expired');
CREATE INDEX orders_created_at_idx ON orders (created_at DESC);

-- Ledger: the idempotency gate. A signature is recorded once, for one order, so it credits at most one order, at most once.
CREATE TABLE payments (
    signature  text        PRIMARY KEY,                                  -- global on purpose
    order_id   uuid        NOT NULL REFERENCES orders (id) ON DELETE RESTRICT,
    outcome    text        NOT NULL CHECK (outcome IN ('credited','late','rejected')),
    lamports   bigint      NOT NULL CHECK (lamports >= 0),               -- credited/claimed transfer sum
    slot       bigint      NOT NULL,
    block_time timestamptz,                                              -- NULL if RPC omitted it (lateness uses detection time)
    reason     text CHECK (reason IN ('tx_failed','no_matching_transfer','balance_mismatch','unfetchable')), -- NULL unless rejected
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT payments_reason_iff_rejected CHECK ((outcome = 'rejected') = (reason IS NOT NULL)),
    CONSTRAINT payments_credit_positive CHECK (outcome <> 'credited' OR lamports > 0)
);
CREATE INDEX payments_order_idx ON payments (order_id, slot);

-- Append-only audit log. Never read to decide state.
CREATE TABLE payment_events (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    order_id   uuid        REFERENCES orders (id) ON DELETE RESTRICT,    -- NULL for rpc.* / watcher.*
    level      text        NOT NULL CHECK (level IN ('info','warn','error')),
    kind       text        NOT NULL,                                     -- Go constants
    message    text        NOT NULL,
    signature  text,
    data       jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX payment_events_order_idx ON payment_events (order_id, id DESC) WHERE order_id IS NOT NULL;
CREATE INDEX payment_events_level_idx ON payment_events (level, id DESC);

COMMIT;
