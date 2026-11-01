-- name: InsertEvent :exec
INSERT INTO payment_events (order_id, level, kind, message, signature, data)
VALUES (sqlc.narg(order_id), @level, @kind, @message, sqlc.narg(signature), sqlc.narg(data));

-- name: ListEvents :many
-- Newest first. Each filter applies only when it is set; before is the id of the last event on the
-- previous page.
SELECT id, order_id, level, kind, message, signature, data, created_at
FROM payment_events
WHERE (sqlc.narg(order_id)::uuid IS NULL OR order_id = sqlc.narg(order_id))
  AND (sqlc.narg(level)::text IS NULL OR level = sqlc.narg(level))
  AND (sqlc.narg(before)::bigint IS NULL OR id < sqlc.narg(before))
ORDER BY id DESC
LIMIT @page_size;
