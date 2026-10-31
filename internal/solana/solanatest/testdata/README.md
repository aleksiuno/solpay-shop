# Recorded devnet transactions

Raw `getTransaction` results from Solana devnet (base64 encoding, confirmed commitment), committed
as recorded. Each one is a transfer between two throwaway wallets, made only for these fixtures and
funded only from the public faucet, with a fresh payment reference attached:

- `transfer_legacy_devpay.json`: a legacy transaction paid with `devpay`.
- `transfer_v1.json`: the same kind of payment as a v1 transaction.
- `transfer_failed.json`: a transfer of more SOL than the payer held, sent without preflight, so
  it failed on chain.

`fixtures.go` lists the signature, the account keys and the balances of each. Any signature can be
looked up in the devnet explorer: `https://explorer.solana.com/tx/<signature>?cluster=devnet`.
