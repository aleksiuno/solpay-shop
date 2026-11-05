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

Interfaces belong to the code that uses them: the payment watcher declares the few chain calls it
needs, and `internal/solana` exports a concrete client and plain types. Test support, the
recorded devnet transactions and deterministic test keys, lives in `internal/solana/solanatest`, so
no test code ships in a production package. The client spaces all its calls, one every 1.25 s, below
the public devnet endpoint's limit of about ten per method per ten seconds. It sorts every failure
into one of five classes: the caller's context ended, rate limited, not indexed yet, permanent (a
retry cannot help) or transient; the genesis check adds a sixth, a cluster that is not devnet. A
keyed endpoint's URL never appears in its errors.

The payment watcher is a goroutine in the same process. Each order has its own reference, a random
32-byte address that the customer's payment lists as an extra account. On each tick the watcher
asks the RPC node for the transactions that mention the reference of each due order that is
pending, underpaid or recently expired, verifies them, and records each result and its events in one database
transaction. The reference is 32 random bytes, not a generated keypair: an account that never
signs needs no private key, so the shop holds no keys at all.

Each order holds the time its next poll is due. A tick takes the orders that have been due
longest, a batch at a time, and gives each order it polls a new time, even when the poll fails, so
every watched order gets its turn however many there are. An order that needs no more polls has
its time cleared, which takes it out of a partial index of the orders still watched, so finding the
due orders reads a few index pages however many orders the shop keeps. Underpaid and expired
orders are watched until an hour past their deadline. Each tick also clears the time of any such
order whose hour ended before its last poll came round, as can happen after downtime, so no order
stays in the index for good. The query that finds the due orders reads the orders table alone.
Reading one order also checks the ledger for a late payment; in a test with 200,000 orders,
putting that check in the due-orders query let a generic plan, which Postgres may cache for a
prepared statement, scan all 150,000 ledger rows.

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
compose the app logs to stdout only, because Docker already keeps it. Every request gets a random
id, returned in the `X-Request-ID` header and added to each line the server logs with the
request's context, so its access log line, events and errors can be matched. The access log leaves
out query strings, because a download link carries its signature in one.

The admin pages and the admin API, under `/admin/` and `/api/v1/admin/`, share one basic auth
check that compares the credentials in constant time. A browser resends basic credentials on its
own, so another site could submit a form to an admin route. Go's `http.CrossOriginProtection`
refuses every cross-site request with an unsafe method before it reaches a route, using the
`Sec-Fetch-Site` and `Origin` headers that browsers send, so no CSRF tokens are needed. curl and
other clients that send neither header pass.

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
towards the amount. When the RPC node gives no block time, the time the payment was first seen
stands in for it. An overpayment counts as paid in full and is logged as a warning. A top-up that
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
names the transactions that never resolved. The one exception to a successful poll is a reference
that the node can never list: after the same hour, its order expires too. An RPC outage or a slow
index therefore delays the expiry, instead of expiring an order that was paid on time.

Every other move is refused, a move to the same status included: for example expired → paid (a late
payment never pays an order), pending → refunded (nothing was received) or any move out of
refunded. `order.Transition` is the one place that lists the moves, and every status change passes
it first. The statement that stores a change is also conditional on the old status
(`UPDATE … WHERE id = $1 AND status = $2`) and must change exactly one row, so two writers that
read the same order cannot both move it. The schema checks only that the status is one of the six
values; it does not encode the moves a second time.

## Payment detection

An order's payments are found through its reference, and `payment.Verify` then works out how much
each transaction pays towards that order. It is a pure function of the transaction, the merchant
address and the reference, so its rules are tested on recorded devnet payments and hand-built
transactions, with no RPC node involved.

A transaction pays an order the sum of its top-level System Program transfers to the merchant that
list the order's reference among their extra accounts, which is where Solana Pay wallets and devpay
put it. Other instructions are ignored wherever they sit, so a wallet may add a compute budget or a
memo, and the transfer need not come last. A transfer to the merchant without the reference does
not count, even in a transaction that pays the order. Legacy, v0 and v1 transactions verify the
same way, and keys that a v0 transaction loads from an address lookup table count like any other.

The sum is then checked against the merchant's balance, which must have grown by at least as much.
That catches a transaction whose payer is the merchant, where the transfer moves nothing and the
balance falls by the fee, and it guards against a transaction that was misread. A transaction that
pays nothing gets a reason, which the payment ledger keeps with it: it failed on chain, no transfer
matched, its transfers claimed more than the balance gained, or it could not be read.

Alternative considered: crediting the merchant's balance change alone. It needs no instruction
decoding, but it would credit the order with everything the merchant received in the transaction,
a transfer for another order or one without the reference included, and nothing would tie the
money to the transfer that lists the reference.

Each poll asks the RPC node for the newest 100 transactions that list the order's reference, at
`confirmed` commitment, with `getSignaturesForAddress`. The watcher skips those the ledger already
holds, and fetches and verifies the rest oldest first, so the transfer that completes the amount is
the one that pays the order. A transaction that failed on chain is rejected from the listing alone,
without being fetched.

Every call to the node waits its turn at the client's limiter, one call every 1.25 s: at most 8 in
10 s, under the 10 per method that the public devnet endpoint reports, with 2 left for a block
explorer in a browser on the same IP. The documented limits, 40 per method and 100 in all per 10 s,
are higher, but the observed one is what counts. A poll costs one `getSignaturesForAddress`, plus
one `getTransaction` for each new signature. A tick polls up to 8 due orders, about one 10 s window
of calls, and when it polls a full batch the next tick starts at once. A round of N due orders
therefore takes about the longer of N × 1.25 s and the poll interval, plus 1.25 s for each new
signature: 10 orders take about 12.5 s, and 35 about 44 s. Underpaid and expired orders past their
deadline are polled once a minute, so they add little to N; a pending order keeps the poll interval
until it expires, two minutes or more after its deadline.

Alternative considered: a webhook from an indexing service. It would report a payment within a
second or two, but it needs an account with the provider, a public URL for the shop, and polling
anyway to catch up on the calls the shop missed while it was down. Polling needs only the public RPC
endpoint, which lists the transactions that mention any account, a reference included.

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

A payment is credited once, however often it is seen. Applying one stores its ledger row first,
with `INSERT … ON CONFLICT (signature) DO NOTHING`; if the signature is already there, the
transaction stores only a `payment.duplicate_ignored` event and changes nothing else. The order's
received total moves only in the transaction that stored the row, so a payment that the watcher
sees again, after a restart or from a second watcher, adds nothing. The row lock does another job:
when two payments to one order are applied at once, the second waits for the first and reads the
total it stored, so neither overwrites the other's credit.

If any step fails, the whole transaction rolls back, and its events are logged only after a
commit. Transactions run at Postgres's default level, READ COMMITTED: the row lock and the
conditional UPDATE are what keep two writers from moving the same order twice.

At READ COMMITTED each statement sees the data as it was when that statement began. That is why a
refund reads the order in a statement after the lock: a locking read that had to wait would return
the newest version of the order's row, but would still miss a late payment that the transaction
holding the lock recorded meanwhile.

## Failure modes

The watcher assumes that the RPC node and the database fail from time to time, and that the shop
itself stops and restarts. None of that may lose a payment or credit one twice.

- The node fails, times out or rate-limits the shop. Each call has a 10 s deadline, the wait at the
  limiter included. The failure is recorded as `rpc.error` or `rpc.timeout` and ends the tick, and
  the watcher backs off: with the default 5 s poll interval, 10, 20 and 40 s, then a minute between
  ticks while the node keeps failing, and at least 15 s after a 429. The first tick that succeeds
  returns to the poll interval.
- The node serves another cluster. The watcher checks the genesis hash before its first poll and
  polls nothing until the check passes. On another cluster the server shuts down and exits 1; a
  check that fails for any other reason is retried like any other call.
- The node lists a transaction before it can return it. The transaction is left for the next poll
  and holds the order's expiry back, as described above, for up to an hour.
- A transaction cannot be used: it failed on chain, the node can never return it, or it does not pay
  the order. It is recorded as rejected, with its reason, and changes nothing.
- A customer pays too little, too much or too late. The order becomes underpaid, paid with the
  surplus logged as a warning, or stays as it is with the payment recorded as late, for a refund.
- A wallet sends a plain transfer without the reference. The shop finds payments only by their
  reference, so it never sees this one.
- The database fails in one order's poll. The failure is recorded as `watcher.error`, the tick goes
  on to the next order, and the order is polled again on its next turn.
- The shop stops in the middle of a tick. The open transaction rolls back, and the next tick finds
  the same transactions and applies each once. On a signal the server first stops taking requests,
  then stops the watcher, and then closes the database pool; what the tick applied before the stop
  stays, and the poll that the stop cuts short records no failure.

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
