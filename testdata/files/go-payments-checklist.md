# Go payments checklist

- Amounts are integers in the smallest unit (lamports, cents), never float64.
- Every money conversion has a table test at 0, 1, the largest value and a value that uses every
  decimal place.
- Status changes go through one function that knows the allowed transitions, and each SQL update is
  conditional on the old status.
- Every external payment id has a unique constraint, and the balance changes only in the
  transaction whose insert wins, so seeing the same payment twice changes nothing.
- A balance change and its audit record are written in one transaction.
- Every network call has a deadline. Transient failures back off and are retried; a failure that
  will never succeed is recorded and skipped, never silently dropped.
- An order expires only after a successful check, so an outage cannot expire an order that was paid.
- Secrets come from the environment, never from code, and never reach a log line.
- Tests cover duplicates, underpayment, overpayment, late payment and a crash between two steps.
