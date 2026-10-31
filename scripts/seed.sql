-- Demo catalogue for local development. Safe to run more than once.
-- Prices stay at or above 0.001 SOL, above the rent-exempt minimum, so even the first payment to a
-- merchant account that was never funded succeeds.
INSERT INTO products (slug, title, description, price_lamports, file_name) VALUES
    ('solana-pay-field-notes', 'Solana Pay field notes',
     'Short notes on Solana Pay transfer requests: the URL format, reference accounts and how a shop finds a payment on chain.',
     1000000, 'solana-pay-field-notes.md'),
    ('go-payments-checklist', 'Go payments checklist',
     'A checklist for payment code in Go: integer amounts, idempotent writes, state machines, timeouts and tests.',
     5000000, 'go-payments-checklist.md')
ON CONFLICT (slug) DO NOTHING;
