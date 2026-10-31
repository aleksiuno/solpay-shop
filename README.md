# solpay-shop

A small Go service that sells digital downloads and accepts SOL on **Solana devnet**.
Devnet only: no real money is ever involved.

A portfolio project by Darius Aleksiūnas. Work in progress towards v0.1.0; the design notes are in
[docs/DESIGN.md](docs/DESIGN.md).

## Quick start

You need Docker with Compose, `make` and `openssl`.

```sh
git clone https://github.com/aleksiuno/solpay-shop.git && cd solpay-shop
make env                         # creates .env with generated secrets
# edit .env: set MERCHANT_ADDRESS to the address of a devnet wallet you control
docker compose up --build -d     # or make up; the app is ready a few seconds later
curl localhost:8080/healthz      # prints ok
```

`make env` prints the ways to get a devnet address; the shop needs only the address, never the
secret key. One of them needs only Docker: it runs this repository's devpay command in the Go
image, which writes a keypair to `data/` and prints its address. Without `make`, run
`cp .env.example .env`, replace every placeholder and set `POSTGRES_PASSWORD` (letters and digits
only).

Compose starts Postgres, applies the migrations, loads two demo products and starts the app on
`127.0.0.1:8080`. If it stops at `migrate`, `docker compose logs migrate` names the setting to fix
(usually `MERCHANT_ADDRESS`). If a local Postgres already uses port 5432, set `DB_PORT` in `.env`.
`docker compose down -v` resets the database, which is also needed after changing
`POSTGRES_PASSWORD`. Secrets and the merchant address live only in `.env`, which is never committed.

## Paying on devnet

Each order will ask for payment with a Solana Pay URL,
`solana:<MERCHANT>?amount=<SOL>&reference=<REF>`, and the shop will find the payment by its
reference alone: the wallet must attach the reference to the transfer as an extra account, so a
plain Send to the merchant address, or `solana transfer`, will not be detected. devpay, a small
command in this repository, pays such a URL on devnet with the reference attached. Until the order
API exists, any address works as `<REF>`, for example one printed by `devpay -new-keypair`.

```sh
go run ./cmd/devpay -new-keypair data/payer.json   # prints the payer's address
# get devnet SOL for that address at https://faucet.solana.com
go run ./cmd/devpay -keypair data/payer.json 'solana:<MERCHANT>?amount=0.001&reference=<REF>'
```

It shows an explorer link for the transaction before sending it and prints the signature once the
cluster confirms the payment. devpay pays on devnet only: it checks the RPC node's genesis hash
before sending anything (`-rpc` picks another devnet endpoint). It refuses a URL without a
reference, and it never overwrites a key file. Without Go, put
`docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD":/src -w /src golang:1.27.3-alpine3.24`
in front of the same `go run` commands. `solana airdrop` no longer works: the devnet RPC faucet is
closed, so use the web faucet. Key files in `data/` are never committed.

## Development

`make help` lists the targets. With Go 1.27, `make run` runs the server on the host against the
compose database (stop the app container first with `docker compose stop app`, as both use port
8080), `make test` runs the unit tests, `make test-integration` adds the Postgres tests (Docker
needed) and `make lint` runs the linters. A failed migration changes nothing, but golang-migrate
marks the schema version dirty and the server refuses to start until it is fixed; in development,
`docker compose down -v` starts over.
