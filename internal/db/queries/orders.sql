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

-- name: UpdateOrderPayment :execrows
-- Stores what a payment did to the order. Like RefundOrder, it changes only an order still in the
-- status the caller read.
UPDATE orders
SET status = @to_status, received_lamports = @received_lamports, paid_at = @paid_at, updated_at = now()
WHERE id = @id AND status = @from_status;

-- name: WatchSet :many
-- Returns up to batch_size orders that are due for a poll at now, the longest due first: pending
-- orders, and underpaid or expired orders whose deadline is after late_cutoff. The status list is
-- the one in orders_watch_idx, written as literals, so that the planner reads that index in
-- next_poll_at order and stops at the limit, whatever the parameters. The query reads the orders
-- table alone: with GetOrder's late-payment check in it, a generic plan could scan the whole
-- ledger.
SELECT * FROM orders
WHERE next_poll_at <= sqlc.arg(now)::timestamptz
  AND status IN ('pending', 'underpaid', 'expired')
  AND (status = 'pending' OR expires_at > sqlc.arg(late_cutoff)::timestamptz)
ORDER BY next_poll_at
LIMIT @batch_size;

-- name: MarkPolled :execrows
-- Sets when the order is next due for a poll. NULL means it needs no more polls.
UPDATE orders SET next_poll_at = @next_poll_at, updated_at = now() WHERE id = @id;

-- name: StopPolling :execrows
-- Clears the next poll time of every underpaid or expired order whose deadline is at or before
-- late_cutoff. WatchSet leaves such orders out, so this is what takes them out of orders_watch_idx
-- when their last poll did not.
UPDATE orders SET next_poll_at = NULL, updated_at = now()
WHERE next_poll_at IS NOT NULL AND status IN ('underpaid', 'expired') AND expires_at <= @late_cutoff;
