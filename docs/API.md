# API reference

The shop's JSON API lives under `/api/v1`. On a local run the base URL is `http://localhost:8080`.

## Conventions

- Responses are JSON (`Content-Type: application/json`), except `/healthz` and downloads.
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
| `not_found` | 404 | no product has the slug or id, no order has the id (or the id is not a UUID), or no endpoint has the path |
| `method_not_allowed` | 405 | the path exists, but not with this method; the `Allow` header lists the methods it has |
| `product_inactive` | 409 | an order for a product that is no longer for sale |
| `slug_taken` | 409 | an admin create or update with a slug that another product has |
| `invalid_transition` | 409 | a refund the order's status does not allow, such as for a pending order, or for an expired one that no payment reached after its deadline |
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
  "download_url": null,
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
- `download_url` is a link to the order's file while the order is `paid` or `fulfilled`, and
  `null` otherwise. It is a path on this server, as in `GET /d/{id}` below. Each read gives a new
  link, which works for 15 minutes.

Errors: `404 not_found` for an unknown id or one that is not a UUID.

### `GET /d/{id}`

Downloads the file of a paid order, by its `download_url`. This is not part of the JSON API: every
answer is plain text or the file.

```sh
curl -fOJ 'localhost:8080/d/<ORDER_ID>?exp=<EXP>&sig=<SIG>'
```

With `-f`, curl reports a refusal instead of saving it as a file; `-J` does not overwrite a file
that is already there.

The link is `/d/{order_id}?exp={expiry}&sig={signature}`. The expiry is a Unix time in seconds,
and the signature is the HMAC-SHA256 of `{order_id}|{expiry}` under `DOWNLOAD_SECRET`, in unpadded
base64url (43 characters). Only the shop can make one. Once the URL is decoded, the id and the
expiry must be written as the shop writes them: an id in upper case, or an expiry with a leading
zero, is refused too.

The file comes as an attachment named after the product's slug, such as
`go-payments-checklist.md`, with `Cache-Control: private, no-store`. `Range` requests work, so a
download can resume while its link does. The first `GET` of a paid order moves it to `fulfilled`;
`HEAD` checks the link and the file without that.

| Status | When |
|---|---|
| `403` | the shop did not make the link, or its order or expiry was changed; or the order is not paid, or was refunded |
| `410` | the link has expired; read the order again for a new one |
| `404` | the order or its file is not there; the order stays as it was |

## Admin endpoints

These need basic auth with `ADMIN_USER` and `ADMIN_PASS`. The admin pages under `/admin/` do the
same in a browser.

### `GET /api/v1/admin/products`

Lists every product, those no longer for sale included, sorted by slug. Each product is as in
`GET /api/v1/products`, with `file_name`, the file in `FILES_DIR` that it sells.

```sh
curl -u "$ADMIN_USER:$ADMIN_PASS" localhost:8080/api/v1/admin/products
```

### `POST /api/v1/admin/products`

Creates a product and answers `201 Created` with it.

```sh
curl -u "$ADMIN_USER:$ADMIN_PASS" localhost:8080/api/v1/admin/products -d '{
  "slug": "go-payments-checklist-team", "title": "Go payments checklist, team licence",
  "description": "The checklist, for a team.", "price_lamports": 20000000,
  "file_name": "go-payments-checklist.md", "active": true}'
```

- `slug` is 1 to 64 characters of `a-z` and `0-9`, in groups joined by single hyphens, and no other
  product may have it.
- `title` is 1 to 120 characters, and `description` at most 2,000; spaces around both are dropped.
- `price_lamports` is the price of one licence, from 1 to 1,000,000,000,000 (1,000 SOL).
- `file_name` names a file in `FILES_DIR`. A product not for sale may keep the file it has after
  the file has gone, so that it can be taken off sale.
- `active` says whether the product is for sale.

Every field is required but `description`. Errors: `400 invalid_request`; `422 validation_failed`,
naming one field that is not acceptable: `active` if it is missing, otherwise the first in the
order above; `409 slug_taken`.

### `PUT /api/v1/admin/products/{id}`

Replaces every field of the product with the id, with a body as for `POST`, and answers `200` with
the product. Errors: as for `POST`, and `404 not_found` for an unknown id or one that is not a UUID.

### `POST /api/v1/admin/orders/{id}/refund`

Marks the order refunded and answers `200` with it. The shop only records the refund: send the
SOL back from the merchant wallet yourself. The body is a JSON object with an optional `note` of
at most 500 characters, which the `order.refunded` event keeps.

```sh
curl -u "$ADMIN_USER:$ADMIN_PASS" localhost:8080/api/v1/admin/orders/<ORDER_ID>/refund \
  -d '{"note": "sent back on request"}'
```

An order can be refunded once it has received something: when it is `underpaid`, `paid` or
`fulfilled`, or `expired` after a payment that came too late. Errors: `409 invalid_transition`
for any other order, `422 validation_failed` for a note that is too long or holds a NUL character,
`404 not_found`, `400 invalid_request` for a body that is not one JSON object.

## Walkthrough

List the products, place an order and read it back, then pay its URL with devpay
(see "Paying on devnet" in the README):

```sh
curl localhost:8080/api/v1/products
curl -i localhost:8080/api/v1/orders -d '{"product_slug": "solana-pay-field-notes", "quantity": 1}'
curl localhost:8080/api/v1/orders/<ORDER_ID>
go run ./cmd/devpay -keypair data/payer.json '<payment.url from the order>'
curl localhost:8080/api/v1/orders/<ORDER_ID>
curl -fOJ 'localhost:8080<download_url from the order>'
```

The order shows `paid` and a `download_url` a few seconds after devpay reports the payment
confirmed.
