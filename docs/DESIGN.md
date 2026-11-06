# Design notes

## What it is

solpay-shop sells digital downloads for SOL on Solana devnet, so no real money is involved. This
small portfolio project shows the parts of a payment system that are easy to get wrong: an explicit
state machine, idempotent payment handling, sensible behaviour when the RPC node fails, and tests
at every layer.

## Architecture

```
  browser, curl
        |
        v
  +--------------------------------------+
  | web (HTML pages)     httpapi (JSON)  |  handlers: decode, call a service, render
  +--------------------------------------+
        |
        v
  +--------------------------------------+
  | product   order   payment   download |  services: business rules, no net/http
  |                  (watcher)           |
  +--------------------------------------+
        |                |
        v                v
  +--------------+  +--------------------+
  | db           |  | solana client      |----> Solana devnet RPC
  | Postgres     |  +--------------------+
  +--------------+
```

Arrows show calls; the admin views also read the ledger and the event log straight from the store.
Services import no `net/http`, pgx or Solana SDK, so their tests need no server, database or
network. Interfaces sit with the code that uses them, such as the watcher's three chain calls;
`internal/db` imports the services to implement the storage ones, never the reverse. In the server
only `internal/solana` imports the Solana SDK. Its fake node, recorded transactions and test keys
live in `internal/solana/solanatest`, outside production code.

- An order's reference is 32 random bytes in base58, not a keypair: an account that never signs
  needs no private key, so the shop holds none.
- Event types live in a leaf package, `audit`, so `order` and `payment` can both create them.
  Events saved in a transaction are logged only after the commit.
- `money` converts lamports and SOL with integers only; `solanapay`, a pure package, builds and
  parses Solana Pay URLs.
- A download link carries an HMAC of the order id and an expiry 15 minutes on, checked before any
  database read. The id alone reads the order, so the signature adds no secret; it makes a copied
  URL expire. A GET fulfils a paid order once the file has opened. Files are read through an
  `os.Root`, and the handler sits in `web`, so `download` needs no `net/http`.
- Logs are JSON on stdout, plus `LOG_FILE` if set; compose logs to stdout only, as Docker keeps it.
- Configuration comes only from the environment. Secrets and the merchant address live only in
  `.env`: `make env` generates the secrets, and compose refuses to start without them.
- The admin pages and the admin API share one basic auth check. A browser resends those
  credentials on its own, so Go's `http.CrossOriginProtection` refuses a cross-site request with an
  unsafe method, which needs no CSRF tokens, and admin answers carry `X-Frame-Options: DENY`.

## Order state machine

```
               paid in full                 first download
  pending ----------------------> paid ----------------------> fulfilled
   |   |                          ^  |                             |
   |   | paid too little          |  | refund                      | refund
   |   v                          |  v                             |
   |  underpaid -- paid in full --+  refunded <--------------------+
   |   |                              ^  ^
   |   +----------- refund -----------+  |
   |                                     |
   | no payment by the deadline          | refund, only if a payment
   v                                     | arrived after the deadline
  expired -------------------------------+
```

| From | To | When | By |
|---|---|---|---|
| pending | paid | an on-time payment brings the total to the amount | watcher |
| pending | underpaid | an on-time payment leaves the total short | watcher |
| pending | expired | no payment by the deadline | watcher |
| underpaid | paid | an on-time top-up brings the total to the amount | watcher |
| paid | fulfilled | the first download | customer |
| underpaid, paid, fulfilled | refunded | an admin marks the order refunded | admin |
| expired | refunded | the same, only if a payment arrived late | admin |

A payment counts only if it is on time: its block time, or without one the time of the poll that
records it, is not after the deadline. An overpayment pays the order with a warning; a late payment
is recorded and changes nothing. Three moves handle money off the simple path:

- underpaid → paid: the customer can send the rest before the deadline. An underpaid order never
  expires, because it holds the customer's SOL.
- fulfilled → refunded: a download does not settle an order for good.
- expired → refunded: the customer is owed a late payment. Without one, the order received nothing
  and cannot be refunded.

`order.Transition` is the one list of moves and refuses all others, such as expired → paid. The
UPDATE that stores a move also names the old status, so of two writers only one moves the order: a
payment or refund that changes no row rolls back, and an expiry or first download changes nothing.
The schema checks values, not moves, so the rules have one copy, in Go.

## Payment detection

Each poll lists the newest 100 transactions for the order's reference, with
`getSignaturesForAddress` at `confirmed`. The reference is public once the QR code is shown, so
anyone can attach junk transactions to it; a listing of 100 keeps a few from pushing the payment
out, and costs one call whatever its length. New signatures are verified oldest first, and a failed
transaction is rejected without being fetched.

`payment.Verify`, a pure function tested on recorded devnet transactions, credits the sum of the
top-level System Program transfers to the merchant that list the reference, in legacy, v0 or v1
transactions. The sum must not exceed the merchant's balance gain, which catches a merchant paying
itself and a misread transaction. A transaction that pays nothing is stored with its reason.

All calls share one limiter, one call every 1.25 s: 8 per 10 s, under the 10 per method observed on
the public devnet endpoint, with 2 left for a block explorer. The documented limits, 40 per method
and 100 in all, are higher. A pending order is due every poll interval (5 s by default), and an
underpaid one until its deadline; then underpaid and expired orders are due each minute until an
hour past the deadline, the late window, so they add little. A tick polls up to 8 due orders, the
longest due first, and a full batch starts the next tick at once. A poll costs one listing plus one
`getTransaction` per new signature, so a round of N due orders takes about the longer of N × 1.25 s
and the poll interval, plus 1.25 s per new signature: 12.5 s for 10 orders, 44 s for 35.

Polling needs only the public RPC endpoint. A plain wallet Send and `solana transfer` cannot attach
a reference, so the repository's devpay pays a Solana Pay URL as a wallet does, on devnet only. A
Solana Pay URL names no cluster, so a mainnet wallet would pay real SOL that the shop never sees;
the order page warns about this, and prices are a few thousandths of a SOL.

## Idempotency

`products` is the catalogue. `orders` holds the status, the amount fixed when the order is placed,
and `received_lamports`, the sum credited. `payments` is the ledger, one row per signature, and
`payment_events` an append-only log, ordered by id, that nothing reads to decide state. Amounts
are integer lamports.

The signature is the ledger's primary key across all orders, so a transaction credits one order,
once. Applying a payment is one transaction: lock the order's row, read it, decide, insert the
ledger row (`ON CONFLICT DO NOTHING`), make the conditional UPDATE, insert the events, commit, then
log them. The watcher skips signatures the ledger holds; one that another watcher stored first adds
only `payment.duplicate_ignored`, and the lock makes a second payment read the first one's total,
so two watchers are still correct. Payments and refunds read
the order in a statement after the lock, so at READ COMMITTED they see the lock holder's commit.

## Failure modes

- RPC down, slow or rate-limited: each call has a 10 s deadline, the limiter's wait included. The
  failure is recorded as `rpc.error` or `rpc.timeout` and ends the tick. The watcher backs off (by
  default 10, 20, 40 s, then a minute), at least 15 s after a 429. A listing that can never
  succeed ends only its order's poll; it is the one failure that lets a pending order expire
  without a successful poll, once the late window ends.
- A failed devnet check: nothing is polled until the genesis hash check passes; on another cluster
  the server exits 1.
- Index lag: a listed transaction that the node cannot return yet waits for the next poll, and a
  pending order expires only after a successful poll at least 2 minutes past its deadline.
- A signature that never resolves holds the expiry back until the late window ends; the expiry
  event names it.
- Duplicates and under, over or late payments follow the rules above.
- A failed or undecodable transaction is stored as rejected and changes nothing.
- A plain Send without the reference is never detected; the order expires.
- A database error in one order's poll is recorded as `watcher.error`; the tick goes on.
- A restart mid-poll rolls the open transaction back; the next tick applies each payment once.
- A failed migration changes nothing, but marks the schema version dirty, and the server refuses
  to start until it is fixed; in development, `docker compose down -v` starts over.
- A crash during a download: fulfilment commits once the file has opened. A fulfilled order still
  downloads, by the same link or a new one from the order page or the API, and a Range request
  resumes the file.

## Out of scope

Not built: accounts, carts, taxes and shipping, mainnet, SPL tokens, wallet adapters, webhooks, a
message queue, file uploads and on-chain refunds. Known limitations:

- Paid orders are not polled, so a second payment is missed unless the poll that paid the order
  lists it too; then it counts as an overpayment.
- A payment after the late window is not detected; the window bounds RPC use.
- A payment followed by 100 or more others before the next poll is missed.
- Transfers made by another program (cross-program invocations) are not credited.

## For production on mainnet

- `finalized` commitment and handling of rolled-back blocks.
- A private RPC endpoint and one scan of the merchant address per tick.
- Custody and key management for the merchant wallet, and on-chain refunds.
- A queue between detection and fulfilment, and rate limiting on order creation.
- Metrics and alerts on watcher lag, RPC errors and stuck orders.
- Secrets from a secret manager instead of `.env`, and a retention policy for the event log.
- Dependabot for Go modules and GitHub Actions, grouped weekly.

## Decisions and alternatives

| Chosen | Alternative | Why |
|---|---|---|
| A ledger, one row per signature | a `paid_signature` column on `orders` | a top-up pays one order with several transactions |
| Polling the public RPC node | webhooks from an indexer | no provider account or public URL; a webhook still needs polling after downtime |
| A reference per order | a memo; a deposit address per order | one call lists a reference's transactions; a memo needs a scan of the merchant's payments, a deposit address a key per order |
| Matching transfers, checked against the balance gain | the balance change alone | it would credit every transfer to the merchant, with or without the reference |
| `confirmed` | `finalized` | a payment shows sooner; a rolled-back block is an accepted risk on devnet |
| One listing per watched reference | one scan of the merchant address | each listing holds only its order's transactions; the scan suits many open orders |
| Migrations at server start, under an advisory lock | a separate job only | `go run ./cmd/server` needs no extra step; compose runs `-migrate-only` first, for the seed |
| An injected `now`, and `testing/synctest` | a clock interface | synctest fakes the standard timers, so the code needs no wrapper |

Go 1.27 is required, for the standard library's `uuid` package, which is new in 1.27.
