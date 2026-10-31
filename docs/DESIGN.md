# Design notes

## What it is

solpay-shop sells digital downloads for SOL. A customer picks a product and gets a payment request:
the merchant address, an exact amount and a deadline. They pay from a devnet wallet. A watcher inside
the service finds the transaction on chain, verifies it and marks the order paid, and the customer
then gets a short-lived, signed download link. Everything runs on Solana devnet, so no real money is
involved.

It is a portfolio project and deliberately small. The aim is to show the parts of a payment system
that are easy to get wrong: an explicit order state machine, idempotent payment handling (a
transaction is credited once, however often it is seen), sensible behaviour when the RPC node is
slow or down, and tests at every layer.

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

Arrows show calls. Handlers call services; services never import `net/http`, pgx or the Solana
SDK, so their tests need no HTTP server, database or network. `internal/db` imports the service
packages to implement their storage interfaces, never the other way round, and `cmd/server` wires
the pieces together. Within the server, only `internal/solana` imports the Solana SDK: the rest of
the code works with plain Go types, and tests can swap in a fake client.

The payment watcher is a goroutine in the same process. Each order has its own reference, a random
32-byte address that the customer's payment lists as an extra account. On each tick the watcher
asks the RPC node for the transactions that mention the reference of each pending, underpaid or
recently expired order, verifies them, and records each result and its events in one database
transaction. The reference is 32 random bytes, not a generated keypair: an account that never
signs needs no private key, so the shop holds no keys at all.

Payment requests are Solana Pay URLs (`solana:<merchant>?amount=…&reference=…`), which a pure
package, `internal/solanapay`, builds and parses. The URL carries the amount in decimal SOL, and
`internal/money` converts between lamports and SOL with integer arithmetic only, because a float64
rounds lamports.

Configuration comes only from the environment. Secrets and the merchant address live only in
`.env`, which is never committed: `make env` generates the secrets, and compose refuses to start
without them.

Logs are JSON lines on stdout, plus an optional file (`LOG_FILE`) for runs on the host. Under
compose the app logs to stdout only, because Docker already keeps it.

The server applies pending migrations when it starts, so `go run ./cmd/server` needs no separate
step. golang-migrate holds a Postgres advisory lock while it runs, so servers that start together
apply each migration once. Compose runs the same image once with `-migrate-only` first, so the
demo seed always finds the schema; the app's own check then finds nothing to apply.

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

An overpayment counts as paid in full and is logged as a warning. Only payments made by the deadline
count towards the amount: a top-up that is still short leaves the order underpaid, and a payment
after the deadline is recorded as late without changing the status. A refund is marked by an admin;
the SOL itself is returned outside the shop.

## Data model

Four tables. `products` holds the catalogue. `orders` holds each order's state: the amount is a
snapshot of price times quantity, and `received_lamports` is the sum of the payments credited to
it. `payments` is the ledger: one row per transaction signature, which is a global primary key, so
a transaction credits at most one order, and only once. `payment_events` is an append-only log
of what happened to each order and what the watcher saw (orders created, payments seen, RPC
errors), ordered by its identity column; nothing reads it to decide state. Amounts are integers in
lamports throughout; SOL appears only in pages and in the payment URL.

Alternative considered: a single `paid_signature` column on `orders`. It cannot represent a top-up,
where one order is paid by several transactions.

## Out of scope

Deliberately not built: user accounts, carts with several products, taxes and shipping, mainnet,
SPL tokens, in-browser wallet adapters, webhooks from indexing services, a message queue, product
file uploads, and refunds sent on chain.

Known limitations:

- Paid orders are no longer watched, so a second payment to a paid order is not detected.
- A payment made more than an hour after the order's deadline is not detected: unpaid orders are
  watched for one hour past their deadline, which keeps RPC use bounded.
- Each poll reads only the newest 100 transactions that mention an order's reference, so a payment
  followed by 100 or more others before the next poll is missed.
- Only top-level System Program transfers count. A transfer made by another program (a
  cross-program invocation) is not credited.

## For production on mainnet

- `finalized` commitment instead of `confirmed`, and handling of rolled-back blocks.
- Custody and key management for the merchant wallet, and refunds sent on chain.
- A queue between payment detection and fulfilment, so each side can retry and scale on its own.
- Metrics and alerts: watcher lag, RPC error rate, orders stuck in one state.
- Rate limiting on order creation.
- A private RPC endpoint, and one scan of the merchant address per tick instead of one query per
  open order.
- Secrets from a secret manager instead of a `.env` file.
- A retention policy for the events log.
- Automated dependency updates for Go modules and GitHub Actions.
