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

## Development

`make help` lists the targets. With Go 1.27, `make run` runs the server on the host against the
compose database (stop the app container first with `docker compose stop app`, as both use port
8080), `make test` runs the unit tests, `make test-integration` adds the Postgres tests (Docker
needed) and `make lint` runs the linters. A failed migration changes nothing, but golang-migrate
marks the schema version dirty and the server refuses to start until it is fixed; in development,
`docker compose down -v` starts over.
