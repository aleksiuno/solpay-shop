# API reference

The shop's JSON API lives under `/api/v1`. On a local run the base URL is `http://localhost:8080`,
or the port that `.env` sets: `APP_PORT` under compose, `APP_ADDR` under `make run`. The examples
leave out `http://`.

## Conventions

- Responses are JSON (`Content-Type: application/json`), except from `/healthz` and downloads:
  these answer in plain text, a wrong method on them included, and a download sends the file.
- A request body is one JSON object of at most 64 KiB. A field the endpoint does not know is
  refused.
- Amounts are integers in lamports: 1 SOL is 1,000,000,000 lamports. SOL appears only in
  `payment.url`, whose `amount` Solana Pay defines in SOL, such as `amount=0.01`. An order's
  `amount_lamports` is at most 10,000,000,000,000 (ten licences at the top price), well below
  2^53, so a JavaScript number holds it exactly.
- Times are RFC 3339 in UTC. They may carry up to six digits of fractional seconds, such as
  `2026-11-03T12:00:00.482113Z`, so parse them as RFC 3339 rather than by a fixed pattern.
- Every response carries an `X-Request-ID` header, a random id that the server also logs as
  `request_id` on each line about the request. Quote it when you report a problem. An
  `X-Request-ID` sent with the request is ignored.
- Every response also carries `X-Content-Type-Options: nosniff` and
  `Referrer-Policy: same-origin`, so a browser takes each answer as the type it is sent as, and
  tells no other site the URL it came from, which may hold an order id.
- Everything under `/api/v1/admin/` and `/admin/` needs HTTP basic auth with `ADMIN_USER` and
  `ADMIN_PASS` from `.env`. Without them, or with others, the answer is `401 unauthorized`. Admin
  answers, the `401` included, carry `X-Frame-Options: DENY` and `Cache-Control: no-store`, since
  the order and event answers list order ids.
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
| `invalid_request` | 400 | the body is empty, larger than 64 KiB or not one JSON object, or has an unknown field or a value of the wrong type (such as a quantity that is not a whole number), or a query parameter has a value the endpoint does not take |
| `validation_failed` | 422 | a value breaks the endpoint's rules, such as a quantity outside 1 to 10, a slug in the wrong form or a file that is not in `FILES_DIR`, or a required field is missing; the message starts with the field's name |
| `not_found` | 404 | no product has the slug or id, no order has the id (or the id is not a UUID), or no endpoint has the path |
| `method_not_allowed` | 405 | the path exists, but not with this method; the `Allow` header lists the methods it has |
| `product_inactive` | 409 | an order for a product that is no longer for sale |
| `slug_taken` | 409 | an admin create or update with a slug that another product has |
| `invalid_transition` | 409 | a refund the order's status does not allow, such as for a pending order, or for an expired one that no payment reached after its deadline |
| `not_payable` | 409 | a transaction for an order that cannot be paid now: one that is not `pending` or `underpaid`, or is past its deadline |
| `unauthorized` | 401 | an admin path without the right basic auth credentials |
| `forbidden` | 403 | a browser request from another origin with an unsafe method |
| `rpc_unavailable` | 503 | the Solana RPC node gave no blockhash for a transaction; the details are logged, never sent, and `Retry-After` says in how many seconds to ask again |
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
  "expires_at": "2026-11-03T12:15:00.482113Z",
  "paid_at": null,
  "download_url": null,
  "created_at": "2026-11-03T12:00:00.482113Z"
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
  do; a plain transfer to the recipient is not detected. A wallet that cannot open the URL, such as
  a browser extension, can get the transaction from `POST /api/v1/orders/{id}/transaction` instead.
  `payment` is `null` once the order can no longer be paid: in any status other than `pending` and
  `underpaid`, and after `expires_at`.
- After `expires_at`, a `pending` order has no `payment` block but stays `pending` while the shop
  makes a final check for payments sent in time. That check takes at least two minutes. It takes
  longer while the RPC node fails, since only a poll that reads the order's reference can expire
  it, and up to an hour after `expires_at` while a transaction that lists the reference cannot be
  read yet. The status then becomes `expired`, or `paid` or `underpaid` if such a payment turns
  up, so keep polling until it changes. An `underpaid` order never expires: a payment sent in time
  that the shop finds within an hour of the deadline still counts, and otherwise only a refund
  changes its status.
- `paid_at` is `null` until the order is paid.
- `download_url` is a link to the order's file while the order is `paid` or `fulfilled`, and
  `null` otherwise. It is a path on this server, as in `GET /d/{id}` below. Each read gives a new
  link, which works for 15 minutes.

Errors: `404 not_found` for an unknown id or one that is not a UUID.

### `POST /api/v1/orders/{id}/transaction`

Builds the transaction that pays what remains of an order, for a wallet to sign and send. It is
for a wallet that cannot open `payment.url`, such as a wallet extension in a desktop browser, which
registers no handler for `solana:` links.

```sh
curl localhost:8080/api/v1/orders/<ORDER_ID>/transaction -d '{"account": "<PAYER>"}'
```

```json
{
  "transaction": "<TRANSACTION>",
  "message": "solpay-shop (devnet): Go payments checklist (2 licences)"
}
```

- `account`, required, is the base58 address of the wallet that pays. It also pays the fee, and is
  the only signer.
- `transaction` is the unsigned transaction in wire format, in base64: the transfer that a wallet
  makes from `payment.url`, of `remaining_lamports` from the account to `payment.recipient`, with
  `payment.reference` as a read-only account. It is a legacy transaction with one zeroed
  signature, which the wallet fills when it signs.
- The transaction names a devnet blockhash at most 10 s old, so devnet takes it for about a minute
  and mainnet never does. Ask again for a new one after that. Each answer pays what remains when it
  is made, so ask again after a payment too.
- `message` says what the payment is for, for the wallet to show: the label and message of
  `payment.url`, joined by a colon.

The body and the answer have the shapes of the `POST` of a Solana Pay transaction request, but this
is not a transaction request endpoint: it has no `GET` for a label and an icon, it sends no CORS
headers, and a browser on another origin is refused, as for any `POST`. Answers carry
`Cache-Control: no-store`.

Errors:

- `400 invalid_request` for a body that is not acceptable (see Conventions);
- `422 validation_failed` for a missing account, one that is not the base58 encoding of 32 bytes,
  or the shop's own address, which cannot pay itself;
- `404 not_found` for an unknown id or one that is not a UUID;
- `409 not_payable` when the order has no `payment` block: it is not `pending` or `underpaid`, or
  it is past `expires_at`;
- `503 rpc_unavailable` when the RPC node gives no blockhash. `Retry-After` says in how many
  seconds the shop asks the node again: a failure is not kept, but the next call starts 2.5 s
  after the failed one began, and none starts for 15 s after the node rate-limited one. Until then,
  a request waits for the next call, or after a rate limit gets the 503 at once.

The checks run in this order, and a request that is wrong in more than one way gets the first
answer that fits: the id (`404`), the body (`400`), the account (`422`), the order (`404`, then
`409`), and last the node (`503`).

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
`go-payments-checklist.md`, with `Cache-Control: no-store`. `Range` requests work, so a
download can resume while its link does. curl does not combine `-C -` with `-J`, so name the file
to resume:

```sh
curl -fC - -o go-payments-checklist.md 'localhost:8080/d/<ORDER_ID>?exp=<EXP>&sig=<SIG>'
```

A download has 10 minutes to send the file; the server then closes the connection, and the command
above fetches the rest while the link works; after that, read the order again for a new one.

The first `GET` of a paid order moves it to `fulfilled`; `HEAD` checks the link and the file
without that.

| Status | When |
|---|---|
| `403` | the shop did not make the link, or its order or expiry was changed; or the order is not paid, or was refunded |
| `410` | the link has expired; read the order again for a new one |
| `404` | the order or its file is not there; the order stays as it was |

## Admin endpoints

Every example in this section reads the credentials from the shell, so load `.env` into it first,
from the repository's root:

```sh
set -a; . ./.env; set +a
```

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
- `title` is 1 to 120 characters, and `description` at most 2,000; spaces around both are dropped,
  and the description's CRLF line breaks are stored as LF, so that each counts as one character.
- `price_lamports` is the price of one licence, from 1 to 1,000,000,000,000 (1,000 SOL).
- `file_name` is the bare name of a file in `FILES_DIR`: at most 255 letters, digits, dots, hyphens
  and underscores, starting with a letter or a digit. The file must be there.
- `active` says whether the product is for sale.

Every field is required but `description`. Errors: `400 invalid_request`; `422 validation_failed`,
naming one field that is not acceptable: `active` if it is missing, otherwise the first in the
order above; `409 slug_taken`.

### `PUT /api/v1/admin/products/{id}`

Replaces every field of the product with the id, with a body as for `POST`, and answers `200` with
the product. A `PUT` that sets `active` to `false` and keeps the product's `file_name` works even
when that file has gone, so that the product can be taken off sale; naming another file needs that
file to be there. curl needs `-X PUT`, since `-d` alone sends `POST`.

```sh
curl -u "$ADMIN_USER:$ADMIN_PASS" -X PUT localhost:8080/api/v1/admin/products/<PRODUCT_ID> -d '{
  "slug": "go-payments-checklist-team", "title": "Go payments checklist, team licence",
  "description": "The checklist, for a team.", "price_lamports": 20000000,
  "file_name": "go-payments-checklist.md", "active": false}'
```

Errors: as for `POST`, and `404 not_found` for an unknown id or one that is not a UUID.

### `GET /api/v1/admin/orders`

Lists the newest orders, at most 100, newest first. `?status=` keeps only the orders in that
status; any other value is `400 invalid_request`.

```sh
curl -u "$ADMIN_USER:$ADMIN_PASS" 'localhost:8080/api/v1/admin/orders?status=paid'
```

```json
{
  "orders": [
    {
      "id": "<ORDER_ID>",
      "status": "paid",
      "product": {"slug": "go-payments-checklist", "title": "Go payments checklist"},
      "quantity": 2,
      "amount_lamports": 10000000,
      "received_lamports": 10000000,
      "reference": "<REF>",
      "has_late_payment": false,
      "expires_at": "2026-11-03T12:15:00.482113Z",
      "created_at": "2026-11-03T12:00:00.482113Z",
      "paid_at": "2026-11-03T12:01:20Z",
      "fulfilled_at": null,
      "refunded_at": null
    }
  ]
}
```

An admin order is what the shop holds, without the `remaining_lamports`, payment request and
download link that `GET /api/v1/orders/{id}` adds. `has_late_payment` is `true` once a payment
with the outcome `late` (see the next endpoint) reached the order; such a payment is never
credited.

### `GET /api/v1/admin/orders/{id}`

Gets one order, as in the list, with two more keys. `payments` lists what each transaction that
names the order's reference did for it, in slot order. `events` lists what happened to the order,
the newest first and at most 100.

```sh
curl -u "$ADMIN_USER:$ADMIN_PASS" localhost:8080/api/v1/admin/orders/<ORDER_ID>
```

The example keeps only `id` and `status` of the keys the list shows, and only the newest event:

```json
{
  "id": "<ORDER_ID>",
  "status": "paid",
  "payments": [
    {"signature": "<SIGNATURE>", "outcome": "credited", "lamports": 10000000, "slot": 4242,
     "block_time": "2026-11-03T12:01:20Z"}
  ],
  "events": [
    {"id": 7, "order_id": "<ORDER_ID>", "level": "info", "kind": "payment.verified",
     "message": "order paid", "signature": "<SIGNATURE>",
     "data": {"amount_lamports": 10000000, "lamports": 10000000, "received_lamports": 10000000},
     "created_at": "2026-11-03T12:01:30.913204Z"}
  ]
}
```

A payment's `outcome` is `credited`, `late` (a valid payment that does not count: made after the
deadline, or to an order that is `expired` or `refunded`) or `rejected`, with a `reason`:
`tx_failed`, `no_matching_transfer`, `balance_mismatch` or `unfetchable`. A rejected payment's
`lamports` is 0, except for `balance_mismatch`, where it is what the transfers claimed.
`received_lamports` can pass `amount_lamports`: an on-time payment is credited in full even when it
is more than what remains, and so is an extra on-time payment found in the same poll as the one
that paid the order.
`block_time` is `null` when the node gave none. Errors: `404 not_found` for an unknown id or one
that is not a UUID.

### `POST /api/v1/admin/orders/{id}/refund`

Marks the order refunded and answers `200` with it, as `GET /api/v1/admin/orders` lists it: its
`status` is `refunded` and `refunded_at` is set. The shop only records the refund: send the SOL
back from the merchant wallet yourself. The body is a JSON object with an optional `note` of at
most 500 characters, which the `order.refunded` event keeps. Send `{}` for no note: a request
without a body is `400 invalid_request`.

```sh
curl -u "$ADMIN_USER:$ADMIN_PASS" localhost:8080/api/v1/admin/orders/<ORDER_ID>/refund \
  -d '{"note": "sent back on request"}'
```

An order can be refunded once it has received something: when it is `underpaid`, `paid` or
`fulfilled`, or `expired` after a payment that came too late. Errors: `409 invalid_transition`
for any other order, `422 validation_failed` for a note that is too long or holds a NUL character,
`404 not_found`, `400 invalid_request` for a body that is not one JSON object.

### `GET /api/v1/admin/events`

Lists the payment event log, a page of at most 100 events, the newest first. Three query
parameters narrow it, each when given: `order_id`, to that order's events; `level`, to the events
at that level, one of `info`, `warn` and `error`; and `before`, to the events older than the one
with that id. An `order_id` that is not a UUID, another `level`, or a `before` that is not a
positive whole number is `400 invalid_request`; an `order_id` that no order has gives an empty
list.

```sh
curl -u "$ADMIN_USER:$ADMIN_PASS" 'localhost:8080/api/v1/admin/events?level=warn'
```

```json
{
  "events": [
    {"id": 9, "order_id": "<ORDER_ID>", "level": "warn", "kind": "payment.late",
     "message": "payment does not count towards the order", "signature": "<SIGNATURE>",
     "data": {"block_time": "2026-11-03T12:16:00Z", "expires_at": "2026-11-03T12:15:00.482113Z",
              "lamports": 10000000, "on_time": false},
     "created_at": "2026-11-03T12:16:05.207519Z"}
  ],
  "next_before": null
}
```

An event is as in `GET /api/v1/admin/orders/{id}`. Its `order_id` is `null` when it concerns no
order, such as a failed devnet check, its `signature` is `""` when it is about no transaction, and
its `data` is `null` when it has no details. `next_before` is the last event's id when the page is
full: pass it as `before`, with the same filters, for the next page. It is `null` on a shorter
page, which is the last; a full page can be the last too, and then the next one is empty.

Each kind of event has a fixed level. `warn`: `payment.underpaid`, `payment.overpaid`,
`payment.late`, `payment.invalid_tx` and `rpc.timeout`. `error`: `rpc.error` and `watcher.error`.
`info`: `order.created`, `order.expired`, `order.refunded`, `payment.seen`, `payment.verified`,
`payment.duplicate_ignored`, `download.issued`, `watcher.started` and `watcher.stopped`.

## Pages

The shop's HTML pages share the API's host but are not part of it. The pages under `/admin/` need
the admin credentials.

| Method | Path | What it shows or does |
|---|---|---|
| `GET` | `/` | the products for sale |
| `GET` | `/p/{slug}` | a product, with the checkout form while it is for sale |
| `POST` | `/checkout` | places an order from the form and answers `303` to the order's page |
| `GET` | `/orders/{id}` | the order: its QR code and wallet link while it can be paid, its download link once paid |
| `GET` | `/orders/{id}/qr.png` | a PNG QR code of `payment.url` while the order can be paid; `404` otherwise |
| `GET` | `/static/style.css` | the style sheet, with an `ETag`; a browser checks its copy on every page and gets `304` while it is the same |
| `GET` | `/admin/` | redirects to `/admin/orders` |
| `GET` | `/admin/orders` | the newest orders, with the `?status=` filter of the API |
| `GET` | `/admin/orders/{id}` | an order with its payments, its events and the refund form |
| `POST` | `/admin/orders/{id}/refund` | refunds the order from the form and answers `303` to its page |
| `GET` | `/admin/events` | the event log, with the filters of `GET /api/v1/admin/events` |
| `GET` | `/admin/products` | every product, those no longer for sale included |
| `GET` | `/admin/products/new` | the form for a new product |
| `POST` | `/admin/products` | creates a product from the form and answers `303` to the list |
| `GET` | `/admin/products/{id}/edit` | the form for a product, filled in |
| `POST` | `/admin/products/{id}` | updates the product from the form and answers `303` to the list |

A path outside `/api/v1/` that no page has gets the shop's error page with `404`, under `/admin/`
once the credentials pass. A wrong method on a page's path gets `405` in plain text, with an
`Allow` header that lists the methods the path has.

## Walkthrough

List the products, place an order and read it back, then pay its URL with devpay (see
[Paying on devnet](../README.md#paying-on-devnet) in the README):

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
