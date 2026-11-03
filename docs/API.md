# API reference

The shop's JSON API lives under `/api/v1`. On a local run the base URL is `http://localhost:8080`.

## Conventions

- Responses are JSON (`Content-Type: application/json`), except `/healthz`, which is plain text.
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
| `not_found` | 404 | no product has the slug, or no endpoint has the path |
| `method_not_allowed` | 405 | the path exists, but not with this method; the `Allow` header lists the methods it has |
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
