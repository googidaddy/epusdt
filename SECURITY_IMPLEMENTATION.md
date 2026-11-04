# Dedicated secure deployment implementation

Updated 2026-10-09. This describes local, uncommitted changes and synthetic acceptance; no RN migration/deployment or live payment has occurred. Start with [the secure operations guide](ops/secure/README.md), not the upstream installer or default Compose file.

## Separate executable and trust boundary

`src/cmd/secure` exposes explicit `init`, `serve`, `health` modes. The secure router registers only four merchant service POSTs plus loopback-only `/healthz` and `/status`; there is no administration page, no Passkey and no Access integration. It registers no legacy password/JWT, installer, manual success, cashier/network switch, EPay/OkPay or Telegram command routes, and never starts the legacy payment-success callback worker. The upstream executable remains for compatibility, not as a secure fallback.

The gateway discovers evidence; the companion merchant authorizes price/destination, displays the trusted same-origin cashier, receives candidates, checks two independently operated RPC sources and atomically consumes/settles each event. Neither gateway success nor a valid callback signature grants merchant funds or goods. The merchant's legacy callback guard stays mandatory for every fixed payment, including damaged/missing snapshots.

## Immutable fixed payment terms

Authenticated create requests use `fixed_payment_intent=true`, USDT with `tron` or `binance`, CNY, an explicit signed `payment_expires_at_ms`, the dedicated PID and business HMAC. A separate `service_secret` signs the exact body, Host/method/path/direction/time/nonce; the nonce must commit to the primary DB before business work. Repeated HTTP requests need a fresh envelope nonce while retaining the same business intent/order ID. The accepted time window is -60/+30 seconds; keep clocks synchronized. All service request bodies are bounded at 64 KiB.

Terms bind merchant/gateway IDs, API-key row, fiat facts, fixed token contract/decimals, recipient, integer raw amount, quote rate and inclusion window in milliseconds. The secure profile pins TRON USDT (6 decimals) and Binance-Peg USDT (18 decimals), uses static USDT/CNY pricing, four-decimal allocation and at most 100 attempts. The merchant independently authorizes decimal rounding and the permitted 0–0.0099 tail. Existing GMPay fiat representation still uses floats; the merchant refuses non-round-tripping values and validates exact integer token terms. This is not a full integer rewrite of every upstream payment path.

The original merchant deadline is used exactly, capped at the gateway's 15-minute maximum; latency and retries cannot extend it. Terms and expiry are persistent. Old fixed requests cannot change network/price/address by editing current settings. Fixed allocation rejects any network/token/address/raw-amount already found in retained history, including expired/soft-deleted records; the companion merchant also has a permanent allocation ledger. Exhausted/overlapping slots refuse creation. Never delete historical allocations or orders to recover capacity; use a reviewed, exclusive new address or a separately designed address pool.

## Discovery, verification and delivery

[Candidate protocol](PAYMENT_CANDIDATES.md) retains observations in the primary DB. TRON scans solid blocks and full block receipts, including indirect Transfer logs. BSC uses WSS hints and finalized/chain-ID-checked 10-block backfill. Persisted height/hash and historical addresses survive restart/configuration changes; missing checkpoints with fixed history, RPC failures or changed finalized checkpoints do not silently jump to head.

The companion merchant preserves historical endpoint/PID/private credentials in `payment_candidate_subscriptions`, ACKs only after a complete SQL batch, rejects changed candidate versions, and independently checks both sources. A separate settlement coordinator re-verifies, enforces lifecycle/ownership and uniquely consumes canonical network/tx/log identity with funds/order/outbox changes in one transaction. SQL default-frozen revision fences creation, cashier, settlement, fulfillment and notification admission; `payment-ops` is OS/DB-authorized and audited, with no force-success action.

Notification-center recipients have durable ACKs; already acknowledged targets are not resent just because another target fails. External success before local ACK can still duplicate messages. Upstream procurement is excluded from new fixed orders; refunds/cancellation notifications retain their existing workflow. Freeze cannot revoke delivered card secrets or previously admitted network requests.

## Management and restoration

There is no management hostname at all. Operator access is an SSH tunnel to the loopback listener, so no remote identity layer, passkey ceremony or Access application exists to misconfigure. Origin bypass prevention (Cloudflare Authenticated Origin Pulls) still requires deployment acceptance.

Operator access is an SSH tunnel to the loopback listener; the profile therefore carries no remote authentication state to manage. Service requests keep the upstream sorted-key HMAC (pid selects the API key) plus the `dujiao-gateway-v1` service MAC bound to direction, Host, method, exact path, body digest, timestamp and a 32-byte nonce; a primary-DB unique nonce is committed before execution and replayed nonces are rejected. Freeze/thaw is not exposed here: it lives in the merchant `payment-ops` command.

`security/revocations.jsonl` persists revocations outside the main DB and fsyncs before the DB change. Restoring a pre-revocation DB cannot restore access while the current ledger is retained. Manual paired snapshot / isolated restore tooling preserves current config, merges revocations, clears old sessions/challenges/grants and resets candidate ACKs for idempotent replay. Missing current security state requires operator recovery review. Reconcile merchant allocations/consumptions/fulfillment since the backup before resuming; chain data cannot reconstruct goods already delivered.

## Runtime, supply chain and verification

[Compose](ops/secure/compose.yaml) uses four explicit host bind mounts, `create_host_path:false`, UID10001, read-only rootfs, drop ALL capabilities, no-new-privileges, loopback port, independent bridge, 384 MiB/0.5 CPU/128 pids and bounded Docker logs. Markers and both databases must already exist for serve. Single-process flock rejects competing instances. AutoMigrate errors stop startup; `init` is only for fresh empty dedicated directories.

Secure outbound clients use HTTPS/WSS public-address DNS pinning, TLS validation, port/target restrictions, no proxy environment or redirects, time/size limits and credential-safe errors. This is application enforcement, not host-level egress filtering or protection from a compromised kernel/root. Main DB and backups still contain sensitive credential/configuration material.

The scratch image builds Go1.26.10 from a fixed base digest (the GO-2026-6809 fix floor) with readonly module resolution. Production build tooling requires clean Git and a complete reviewed commit image tag, uses a separate resource-limited builder, and never switches services automatically. Fork workflows do not automatically publish to upstream namespaces or sync old UI artifacts. Review module changes and actual image identity on every release.

Local verification: full gateway Go suite (19 test packages), four critical race packages, service-request replay/nonce/signature attacks, two-network synthetic merchant HTTP creation/retry/deadline, final-image Compose recreation/persistence/missing mounts/process lock/online bootstrap, and Python paired snapshot plus real age roundtrip with simulated SCP. Companion merchant full suite (196 test packages), architecture/race, real isolated PostgreSQL16.14/Redis7.4.9 migrations/rollback/queue recovery and 67 frontend tests/build passed. No actual TLS/mainnet/browser/Cloudflare/Telegram/customer payment acceptance is claimed.

Final `govulncheck` on `./cmd/secure` found no reachable call trace, with remaining module/package findings GO-2026-5932 (x/crypto) and GO-2026-5506 (OpenTelemetry). Track these and rerun after changes; absence of a trace is not a guarantee of zero vulnerabilities. Real mainnet anchors/sources/budgets (especially full TRON scanning on a free plan), AOP enforcement, RN resource/ingress checks and paired live recovery remain deployment gates. See [RPC acceptance](ops/secure/rpc-acceptance.md).
