-- name: CreateOrder :exec
-- The new order is due for its first poll at once.
INSERT INTO orders (id, product_id, quantity, amount_lamports, reference, status, expires_at,
                    next_poll_at, created_at, updated_at)
VALUES (@id, @product_id, @quantity, @amount_lamports, @reference, @status, @expires_at,
        sqlc.arg(created_at)::timestamptz, sqlc.arg(created_at)::timestamptz,
        sqlc.arg(created_at)::timestamptz);

-- name: GetOrder :one
SELECT sqlc.embed(o), p.slug AS product_slug, p.title AS product_title,
       EXISTS (SELECT 1 FROM payments pm WHERE pm.order_id = o.id AND pm.outcome = 'late') AS has_late_payment
FROM orders o
JOIN products p ON p.id = o.product_id
WHERE o.id = @id;

-- name: ListOrders :many
-- Newest first. The status filter applies only when it is set.
SELECT sqlc.embed(o), p.slug AS product_slug, p.title AS product_title,
       EXISTS (SELECT 1 FROM payments pm WHERE pm.order_id = o.id AND pm.outcome = 'late') AS has_late_payment
FROM orders o
JOIN products p ON p.id = o.product_id
WHERE (sqlc.narg(status)::text IS NULL OR o.status = sqlc.narg(status))
ORDER BY o.created_at DESC, o.id DESC
LIMIT @page_size;

-- name: LockOrder :one
-- Locks the order's row until the transaction ends. Read the order in a later statement, which
-- sees what committed while this one waited, such as a late payment.
SELECT id FROM orders WHERE id = @id FOR UPDATE;

-- name: RefundOrder :execrows
-- Changes only an order that is still in the status the caller read, so a stale read changes no
-- row.
UPDATE orders
SET status = @to_status, refunded_at = @refunded_at, updated_at = now()
WHERE id = @id AND status = @from_status;
