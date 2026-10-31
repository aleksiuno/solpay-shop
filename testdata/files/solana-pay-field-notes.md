# Solana Pay field notes

A Solana Pay transfer request is a URL that a wallet turns into a transfer:

    solana:<recipient>?amount=<SOL>&reference=<address>&label=<shop>&message=<text>

- `recipient` is the merchant's base58 address. Without an `spl-token` parameter the transfer is
  native SOL.
- `amount` is a decimal number of SOL. A wallet rejects an amount with more decimal places than SOL
  has (9), so a shop converts from lamports with integer maths: 1 SOL is 1,000,000,000 lamports.
- `reference` is a 32-byte value in base58, picked at random for each order so that it is unique.
  The wallet attaches it to the transfer instruction as a read-only account that does not sign.
  It need not be a real key and nobody holds a private key for it: it only makes the transaction
  findable.
- A shop finds the payment with `getSignaturesForAddress(reference)`, fetches each transaction with
  `getTransaction`, and checks that it succeeded and that a transfer carrying the reference paid
  the right recipient at least the expected amount.
- `confirmed` commitment is quick and enough for a demo. A shop that ships goods of real value waits
  for `finalized`.
- The URL names no cluster, and an address is the same on every cluster. A wallet set to mainnet
  would send real SOL to that address on mainnet, so a devnet shop tells the customer to switch
  the wallet to devnet first.
