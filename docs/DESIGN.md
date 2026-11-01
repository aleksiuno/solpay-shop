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

Interfaces belong to the code that uses them: the payment watcher will declare the few chain calls
it needs, and `internal/solana` exports a concrete client and plain types. Test support, the
recorded devnet transactions and deterministic test keys, lives in `internal/solana/solanatest`, so
no test code ships in a production package. The client spaces all its calls, one every 1.25 s, below
the public devnet endpoint's limit of about ten per method per ten seconds. It sorts every failure
into one of five classes: the caller's context ended, rate limited, not indexed yet, permanent (a
retry cannot help) or transient; the genesis check adds a sixth, a cluster that is not devnet. A
keyed endpoint's URL never appears in its errors.

The payment watcher is a goroutine in the same process. Each order has its own reference, a random
32-byte address that the customer's payment lists as an extra account. On each tick the watcher
asks the RPC node for the transactions that mention the reference of each pending, underpaid or
recently expired order, verifies them, and records each result and its events in one database
transaction. The reference is 32 random bytes, not a generated keypair: an account that never
signs needs no private key, so the shop holds no keys at all.

The event log has a package of its own, `internal/audit`, which imports no other package of the
shop. The order, payment and download code all emit events. Most arise in payment, but the event
types cannot live there: order would have to import payment, which already imports order. In order,
they would put the watcher's RPC errors in a package that has nothing to do with RPC. Code that
changes an order in a database transaction saves its events in the same transaction and logs them
only after the commit, so the log never shows a change that rolled back. Events outside any
transaction, such as an RPC error, are saved and logged at once, and logged even if saving fails.
An outbox table that a separate process publishes from would also keep the log and the database in
step, but nothing here consumes events, so saving them in the order's own transaction is enough.

Payment requests are Solana Pay URLs (`solana:<merchant>?amount=…&reference=…`), which a pure
package, `internal/solanapay`, builds and parses. The URL carries the amount in decimal SOL, and
`internal/money` converts between lamports and SOL with integer arithmetic only, because a float64
rounds lamports.

Neither a plain wallet Send nor `solana transfer` can attach a reference, so the repository
includes devpay, a small command that pays a Solana Pay URL on devnet the way a Solana Pay wallet
does: the references go on the transfer as read-only accounts. It checks the RPC node's genesis
hash before it sends anything, so it cannot pay on mainnet by mistake.

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

| From | To | When | By |
|---|---|---|---|
| pending | paid | an on-time payment brings the total to the amount or more | watcher |
| pending | underpaid | an on-time payment leaves the total short of the amount | watcher |
| pending | expired | nothing was paid by the deadline | watcher |
| underpaid | paid | an on-time top-up brings the total to the amount or more | watcher |
| paid | fulfilled | the first authorised download | customer |
| underpaid, paid, fulfilled | refunded | an admin marks the order refunded | admin |
| expired | refunded | the same, but only if a payment arrived after the deadline | admin |

A payment is on time if its block time is not after the deadline, and only on-time payments count
towards the amount. An overpayment counts as paid in full and is logged as a warning. A top-up that
is still short leaves the order underpaid, and a payment after the deadline is recorded as late
without changing the status. A refund is marked by an admin; the SOL itself is returned outside the
shop.

Three of the moves handle money that does not fit the simple path:

- underpaid → paid: a customer who sent too little can send the rest, with the same reference,
  before the deadline.
- fulfilled → refunded: a download does not settle the order for good, so an admin can still
  refund it.
- expired → refunded: a payment that arrives after the deadline is recorded but not credited, so
  the order stays expired and the customer is owed that SOL. The refund closes the order. An
  expired order without a late payment received nothing, so it cannot be refunded.

An underpaid order never expires, because it holds the customer's SOL: it stays underpaid until a
top-up pays it or an admin refunds it. A pending order expires late on purpose. The watcher expires
it no sooner than two minutes after the deadline, and only right after a poll of its reference that
succeeded. A transaction that the RPC node lists for the reference but cannot return yet also holds
the expiry back, for up to an hour after the deadline; after that the order expires, and its event
names the transactions that never resolved. An RPC outage or a slow index therefore delays the
expiry, instead of expiring an order that was paid on time.

Every other move is refused, a move to the same status included: for example expired → paid (a late
payment never pays an order), pending → refunded (nothing was received) or any move out of
refunded. `order.Transition` is the one place that lists the moves, and every status change passes
it first. The statement that stores a change is also conditional on the old status
(`UPDATE … WHERE id = $1 AND status = $2`) and must change exactly one row, so two writers that
read the same order cannot both move it. The schema checks only that the status is one of the six
values; it does not encode the moves a second time.

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

## Transactions

Every change to an order is one database transaction that also saves the events it causes, and
the code for these transactions lives in `internal/db`. The services decide what should happen;
the store makes it atomic.

- Placing an order inserts the order and its `order.created` event.
- A refund locks the order's row (`SELECT … FOR UPDATE`) and then reads the order in a second
  statement, which lets the order service check the status and whether a late payment arrived.
  It then moves the order to refunded with `UPDATE … WHERE id = $1 AND status = $2`, which must
  change exactly one row, and inserts the `order.refunded` event.
- Applying a payment, expiring an order and marking it fulfilled follow the same pattern. The
  status change passes `order.Transition`, the UPDATE names the old status, and the events, plus
  the ledger row for a payment, go in the same transaction. Applying a payment locks the row too,
  because it reads the order before it decides; expiry and fulfilment rely on the conditional
  UPDATE alone.

If any step fails, the whole transaction rolls back, and its events are logged only after a
commit. Transactions run at Postgres's default level, READ COMMITTED: the row lock and the
conditional UPDATE are what keep two writers from moving the same order twice.

At READ COMMITTED each statement sees the data as it was when that statement began. That is why a
refund reads the order in a statement after the lock: a locking read that had to wait would return
the newest version of the order's row, but would still miss a late payment that the transaction
holding the lock recorded meanwhile.

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
