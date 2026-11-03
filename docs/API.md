# API reference

The shop's JSON API lives under `/api/v1`. On a local run the base URL is `http://localhost:8080`.

## Conventions

- Responses are JSON (`Content-Type: application/json`), except `/healthz`, which is plain text.
- A request body is one JSON object of at most 64 KiB. A field the endpoint does not know is
  refused.
- Amounts are integers in lamports: 1 SOL is 1,000,000,000 lamports. The API never uses
  fractional SOL.
- Every response carries an `X-Request-ID` header, a random id that the server also logs as
  `request_id` on each line about the request. Quote it when you report a problem. An
  `X-Request-ID` sent with the request is ignored.
- Everything under `/api/v1/admin/` needs HTTP basic auth with `ADMIN_USER` and `ADMIN_PASS` from
  `.env`. Without them, or with others, the answer is `401 unauthorized`.
- A browser request from another origin (another scheme, host or port) with an unsafe method
  (`POST`, `PUT`, `PATCH`, `DELETE`) is refused with `403 forbidden`. curl and other clients that
  send no `Sec-Fetch-Site` or `Origin` header are not affected. The API sends no CORS headers, so a
  page from another origin cannot read its responses either.

## Errors

Every error from the API has the same shape:

```json
{"error": {"code": "not_found", "message": "no such product"}}
```

The `code` is for programs and does not change its meaning; the `message` is for people and may
change.

| Code | Status | When |
|---|---|---|
| `invalid_request` | 400 | the body is empty, larger than 64 KiB or not one JSON object, or has an unknown field or a value of the wrong type (such as a quantity that is not a whole number) |
| `validation_failed` | 422 | a value is out of range, such as a quantity outside 1 to 10, or a required field is missing |
| `not_found` | 404 | no product has the slug, no order has the id (or the id is not a UUID), or no endpoint has the path |
| `method_not_allowed` | 405 | the path exists, but not with this method; the `Allow` header lists the methods it has |
| `product_inactive` | 409 | an order for a product that is no longer for sale |
| `unauthorized` | 401 | an admin path without the right basic auth credentials |
| `forbidden` | 403 | a browser request from another origin with an unsafe method |
| `internal` | 500 | anything else; the details are logged, never sent |

## Endpoints

### `GET /healthz`

Checks that the server is up and its database answers within 2 s. Answers `200` with `ok`, or
`503` with `database unavailable`, both as plain text.

```sh
curl localhost:8080/healthz
```

### `GET /api/v1/products`

Lists the products for sale, sorted by slug.

```sh
curl localhost:8080/api/v1/products
```

```json
{
  "products": [
    {
      "id": "<PRODUCT_ID>",
      "slug": "go-payments-checklist",
      "title": "Go payments checklist",
      "description": "A checklist for payment code in Go: integer amounts, idempotent writes, state machines, timeouts and tests.",
      "price_lamports": 5000000,
      "active": true
    }
  ]
}
```

`price_lamports` is the price of one licence. With nothing for sale, `products` is an empty list.

### `GET /api/v1/products/{slug}`

Gets one product by its slug. A product that is no longer for sale is returned too, with `active`
set to `false`.

```sh
curl localhost:8080/api/v1/products/go-payments-checklist
```

The response is one product, as in the list above. Errors: `404 not_found` for an unknown slug.

### `POST /api/v1/orders`

Places an order for one to ten licences of a product.

```sh
curl -i localhost:8080/api/v1/orders -d '{"product_slug": "go-payments-checklist", "quantity": 2}'
```

Both fields are required. The answer is `201 Created`, with the new order's path in the
`Location` header and the order as in `GET /api/v1/orders/{id}`. Errors: `400 invalid_request`,
`422 validation_failed`, `404 not_found` for an unknown product, `409 product_inactive`.

### `GET /api/v1/orders/{id}`

Gets an order. The id is the order's only key: anyone who has it can see the order, so keep it
private. Order responses carry `Cache-Control: no-store`.

```sh
curl localhost:8080/api/v1/orders/<ORDER_ID>
```

```json
{
  "id": "<ORDER_ID>",
  "status": "pending",
  "product": {"slug": "go-payments-checklist", "title": "Go payments checklist"},
  "quantity": 2,
  "amount_lamports": 10000000,
  "received_lamports": 0,
  "remaining_lamports": 10000000,
  "payment": {
    "recipient": "<MERCHANT>",
    "reference": "<REF>",
    "url": "solana:<MERCHANT>?amount=0.01&reference=<REF>&label=solpay-shop%20%28devnet%29&message=Go%20payments%20checklist%20%282%20licences%29"
  },
  "expires_at": "2026-11-03T12:15:00Z",
  "paid_at": null,
  "created_at": "2026-11-03T12:00:00Z"
}
```

- `status` is one of `pending` (waiting for payment), `underpaid` (paid on time, but less than the
  amount), `paid`, `fulfilled` (paid and downloaded), `expired` (nothing paid by the deadline) and
  `refunded`.
- `amount_lamports` is the price times the quantity when the order was placed; a later price
  change does not alter it. `received_lamports` is what on-time payments have brought in, and
  `remaining_lamports` what is still to pay, never below 0.
- `payment` says how to pay what remains: the Solana Pay URL, and the recipient and reference it
  names. The payment must list the reference as an extra account, as Solana Pay wallets and devpay
  do; a plain transfer to the recipient is not detected. `payment` is `null` once the order can no
  longer be paid: in any status other than `pending` and `underpaid`, and after `expires_at`, even
  while the status is still `pending`.
- Times are in UTC. `paid_at` is `null` until the order is paid.

Errors: `404 not_found` for an unknown id or one that is not a UUID.

## Walkthrough

List the products, place an order and read it back, then pay its URL with devpay
(see "Paying on devnet" in the README):

```sh
curl localhost:8080/api/v1/products
curl -i localhost:8080/api/v1/orders -d '{"product_slug": "solana-pay-field-notes", "quantity": 1}'
curl localhost:8080/api/v1/orders/<ORDER_ID>
go run ./cmd/devpay -keypair data/payer.json '<payment.url from the order>'
```
