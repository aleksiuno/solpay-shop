-- name: ListProducts :many
SELECT * FROM products
WHERE active OR NOT @active_only::boolean
ORDER BY slug;

-- name: GetProductBySlug :one
SELECT * FROM products WHERE slug = @slug;

-- name: GetProductByID :one
SELECT * FROM products WHERE id = @id;

-- name: CreateProduct :one
INSERT INTO products (slug, title, description, price_lamports, file_name, active)
VALUES (@slug, @title, @description, @price_lamports, @file_name, @active)
RETURNING *;

-- name: UpdateProduct :one
UPDATE products
SET slug = @slug, title = @title, description = @description, price_lamports = @price_lamports,
    file_name = @file_name, active = @active, updated_at = now()
WHERE id = @id
RETURNING *;
