# Fixed-payment candidate protocol and recovery

Updated 2026-10-09. The gateway is a discovery/delivery service, **not permission to fulfill an order**. The companion merchant now implements persistent receipt/ACK, independent verification, ownership, unique consumption, atomic settlement and SQL effects. Activation remains default-disabled pending real deployment acceptance. Use [the dedicated secure profile](ops/secure/README.md).

## Stored observations and scan history

`payment_candidates` is in the gateway primary DB. It saves source merchant/gateway order identity, network, transaction hash, block height/hash/time, contract, recipient and integer amount. `evm_log` and `tron_log` include receipt/log event index; EVM observations also carry `removed`. Older `tron_calldata` rows can remain historical hints, but the current TRON scanner reads full solid block receipts and emits actual Transfer logs, including indirect calls. No calldata alone establishes receipt.

A digest deduplicates identical observations per order. Different block/removal/evidence versions append history; they do not erase previous facts. This is not canonical event consumption. The merchant derives network/tx/receipt-log identity independently from two sources and enforces unique consumption in its own ledger.

Fixed lookup includes expired and soft-deleted history and scans historical addresses even when current wallet configuration disables/deletes them. Float order values only find possible slots; saved raw integer amounts must match. Late, removed, ambiguous, missing-time or inconsistent-term observations are retained for review. Transfers outside known fixed slots are not a complete unmatched-funds accounting ledger; users handle unclear/late payments manually.

- TRON persists height/hash in `tron_scan_checkpoints`. Solid block and all receipts must be available; successful processing precedes checkpoint advance. Crashes replay and deduplicate. A changed checkpoint or lost checkpoint with fixed history stops progress instead of jumping forward. A clean first start begins at current solid head minus one.
- BSC WSS only provides hints; missing block time stays unknown. Backfill checks chain ID56 and finalized, validates the saved canonical hash, queries at most 10 blocks and saves observations before advancing. Primary scan-health and runtime cursor are coordinated by the single process; backup must preserve both DBs. Changed finalized history/failed RPC cannot be skipped.
- `payment_scan_health` records polled/chain/scanned state. Secure creation requires head/scanned gap at most 20, head time at most two minutes old and polling at most thirty seconds old. This is a discovery availability gate, not proof of merchant independent verification.

Existing fixed-order ordinary/manual-success updates remain blocked. Only the companion merchant's independent settlement grants payment/delivery. The secure executable never starts the upstream callback worker.

## Authenticated service requests

The four exact POST endpoints share `/payments/gmpay/v1/order/`:

| Suffix | Operation |
| --- | --- |
| `create-transaction` | Create/recover a fixed payment with original absolute deadline |
| `transfer-candidates` | List up to 100 unacknowledged records |
| `transfer-candidates/ack` | Acknowledge durable merchant receipt of a batch |
| `scan-health` | Read scanner progress for merchant operational monitoring |

All requests preserve the existing sorted scalar key=value HMAC-SHA256 signature, with `pid` selecting the API-key row. The dedicated profile additionally requires `dujiao-gateway-v1` service MAC using an independent secret. It binds direction `merchant->gateway`, Host, method, exact path, original body digest, timestamp and 32-byte random nonce; query/alternate raw path are rejected. Time tolerance is -60/+30 seconds. A primary-DB unique nonce is committed before execution; retry uses a fresh nonce and the same business identity. Request bodies are capped at 64 KiB. Do not put secrets in URLs or frontend code.

List request body (before real signatures/envelope):

```json
{"pid":"<dedicated PID>","action":"transfer_candidates.list.v1","signature":"<business HMAC>"}
```

The normal response `data` is an array of up to 100 oldest unacknowledged rows. IDs are canonical JSON decimal strings, including values above JavaScript safe integers. There is no high-water cursor that skips an earlier late-committed ID. API-key ownership is determined by authentication, not a caller-supplied database ID.

ACK body:

```json
{"pid":"<dedicated PID>","action":"transfer_candidates.ack.v1","ids":"123,124","signature":"<business HMAC>"}
```

`ids` is a signed CSV scalar, 1–100 unique positive uint64 IDs. Invalid, missing or foreign rows reject the entire ACK. Repeating a committed ACK is harmless. `received:true` means delivery receipt only; records are not deleted or marked paid.

Health body uses signed `action=scan_health.v1`; see the merchant adapter `candidate.go` for current response handling. Authentication does not make reported scanner state or candidate facts trustworthy enough to settle.

## Merchant persistence and credentials

The merchant stores the complete list batch in a single SQL transaction before ACK. Receipt identity is normalized endpoint/PID plus remote ID. Repeated evidence deduplicates; altered versions persist and flag permanent conflict. If SQL fails no ACK is sent; if ACK response disappears, replay is safe. Candidate IDs/amounts/recipient/time are never trusted as authority over merchant authorization.

`payment_candidate_subscriptions` is saved with authorization and retains historical source/PID/private configuration, including business and service credentials. Channel deletion/disable/rotation does not discard it. These rows and backups are as sensitive as channel config. The secure gateway enables only its current PID, so rotating PID/secrets requires freezing, draining/reconciling in-flight work and a reviewed migration/old-source availability plan. Do not delete old subscriptions to silence failing polls.

The merchant polls subscriptions every minute, records attempt/success/scanner-health and alerts on stale/unhealthy subscriptions. Candidate ingestion and independent verification continue while payment SQL is frozen; creation/binding/settlement/delivery/send admission obey the SQL revision fence. Persisted `chain_checked` alone is never permission to consume funds.

## Backup and replay

Manually freeze merchant → stop gateway → consistent merchant DB snapshot → both gateway DBs/config/revocations/manifest. Keep candidate history, allocations, subscriptions and runtime checkpoints; these are financial records, not cache. The scripts never schedule backups or automatically thaw payments.

Restoring an older merchant DB may predate gateway ACKs. [restore-prepare.py](ops/secure/restore-prepare.py) prepares a new isolated gateway directory, clears candidate ACK timestamps without erasing evidence, removes old sessions/challenges/grants and merges current revocations. Restore merchant separately and freeze before API/worker start, reconcile already delivered/consumed events in the backup gap, then replay. Blockchain scans cannot reconstruct lost fulfillment or attribution history. Missing/corrupt scan history must be investigated, not repaired by silently moving to head.

## Verification and deployment boundary

Local gateway full suite: 19 packages; critical race: four packages. Tests cover concurrent deduplication, reopen, source/ACK isolation, lost ACK, changed evidence, failed receipts, indirect TRON Transfer, checkpoint failure/restart/hash changes, BSC 10-block finalized recovery and wrong chain. Companion tests include real isolated PostgreSQL/Redis settlement/outbox/consumer failure recovery and two-network synthetic HTTP creates with precise deadlines. See [implementation](SECURITY_IMPLEMENTATION.md).

Real providers, finalized/solidity methods, quota and long disconnect recovery remain unaccepted. Full TRON scanning consumes quota even with zero orders; free-plan budget and second independent source are explicit [deployment gates](ops/secure/rpc-acceptance.md). Real browser, Access MFA, RN infrastructure and natural customer transactions are separate acceptance evidence.
