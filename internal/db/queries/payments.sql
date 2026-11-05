-- name: InsertPayment :one
-- Records what a transaction did for an order. The signature is the key across all orders, so a
-- transaction already in the ledger, for this order or another, inserts nothing and returns no row.
INSERT INTO payments (signature, order_id, outcome, lamports, slot, block_time, reason)
VALUES (@signature, @order_id, @outcome, @lamports, @slot, @block_time, @reason)
ON CONFLICT (signature) DO NOTHING
RETURNING signature;

-- name: KnownSignatures :many
-- Returns those of the signatures that are already in the ledger.
SELECT signature FROM payments WHERE signature = ANY(@signatures::text[]);

-- name: ListPaymentsByOrder :many
-- Returns the order's ledger rows in the order of their slots, the first seen first among rows of
-- one slot.
SELECT * FROM payments WHERE order_id = @order_id ORDER BY slot, created_at, signature;
