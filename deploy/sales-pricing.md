# Managed relay sales pricing

All provider-managed price snapshots retain upstream cost. Retail charging uses
1.25 times that cost (upstream 0.8, retail 1), with the existing user/routing-group
discount applied. Synchronizing/importing a price never compounds the markup.

`pkg/mujianpricing` owns the multiplier. Preauthorization uses decimal arithmetic
and rounds upward; final settlement multiplies the unrounded total (including
supported cache, quantity and tool charges) and rounds once to integer quota.
Upstream invoices independently round their own quota, so a one-unit difference
from multiplying an already rounded upstream invoice is expected.

Managed final usage is not capped at the initial estimate. Atomic settlement may
produce wallet/token debt or subscription overage, blocking later requests until
funding is available. Failed requests refund their reservation. Legacy routes
without a managed provider price snapshot retain their prior behavior.

Product catalog/pricing/selector rates are retail rates. Provider administration
continues to expose cost snapshots. New consumption log metadata includes
`sales_ratio` and `price_provider`; historical logs without these fields retain
their original presentation. Top-up conversion and historical balances are not
changed. No database migration is required.

## Validation and rollout

- Simplify and review the changed scope, then run `go test ./...`, affected-package
  `go vet`, frontend pricing tests, formatting checks and production build.
- Probe each enabled configured provider through the relay. Check wallet, token,
  ledger and consumption log, failure refunds, streaming and the product UI.
- Back up production data and runtime configuration before deploying. Roll back
  the application image to obs.7 if health/smoke checks fail; do not restore the
  database over newer user transactions.
