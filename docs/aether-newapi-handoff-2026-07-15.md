# New API x AETHER implementation handoff

Generated: 2026-07-15 (Asia/Shanghai)

This handoff is for continuing the active New API x AETHER integration goal in a new Codex window whose workspace root includes both repositories.

## 1. Workspace to open

Open `D:\workspaces` (or a higher directory) as the workspace root so the new window has read/write access to both:

- `D:\workspaces\new-api`
- `D:\workspaces\Aether-pureai`

Both repositories were freshly confirmed on:

```text
codex/aether-newapi-integration
```

Both worktrees contain extensive uncommitted work. Preserve all of it. Do not reset, checkout over files, clean, stage, commit, or push unless the user separately asks.

## 2. Product boundary that must not change

- New API is the only user-facing financial authority: users, top-ups, refunds, subscriptions, balances, pre-consume, settlement, and user pricing remain in New API.
- AETHER is a visible New API channel and an upstream aggregation, cost, profit, health, balance, Provider Pool, and Routing Profile service.
- AETHER may consume anonymized, read-only New API events and maintain its own analytics ledger. It must never modify New API user financial data.
- Ordinary New API channels remain independent. Never send their upstream keys, user API keys, payment information, real identity, or balance details to AETHER.
- The AETHER channel must reuse AETHER's existing proxy, API-key authentication, Provider Catalog, Pool, Routing Profile, SSE, retry, and Usage Settlement path. Do not build a second isolated forwarding engine.
- Only `direct_channel` may execute a real upstream request. `parallel_shadow` and `aether_decision` remain reserved and fail closed until fully implemented and gated.

## 3. Current repository state

### New API

Fresh `git status` shows many modified and untracked files. Important integration areas now present include:

- Native AETHER channel constants and OpenAI-compatible adapter wiring.
- `AetherIntegration` configuration, encrypted control/relay secrets, revisions, status, and controller endpoints.
- Relay context HMAC headers and verification.
- Restricted AETHER service authentication with nonce replay protection; Redis failure is intended to fail closed.
- Capabilities/config/status synchronization with typed errors, response limits, no redirects, and reserved-mode rejection.
- `/api/aether/v1/pricing`, `/events`, and `/snapshot` contracts.
- AETHER outbox model and transaction-aware event APIs.
- AETHER channel editor section and i18n entries in the default frontend.

The Tx APIs named by the original red test now exist in `model/aether_ledger.go`:

```text
RecordAetherUsageEventTx
RecordAetherSubscriptionEventTx
RecordAetherChannelEventTx
RecordAetherChannelBalanceObservationTx
RecordAetherPricingEventTx
```

Meaningful regression tests now exist for:

- rollback of top-up, subscription, channel, pricing, consume-log, and task-refund mutations;
- outbox retry dedupe and independent mutation IDs;
- subscription lifecycle events;
- snapshot pagination, full-window totals, ETag, historical confidence, and time bounds;
- capabilities/config/status synchronization;
- relay signature safety and service replay protection.

Fresh source inspection also shows:

- snapshot page events are ordered by ID;
- full-window snapshot aggregation uses `Rows()` streaming;
- pricing reads through `billing_setting.GetPricingSnapshot()` / `ratio_setting.GetPricingSnapshot()`;
- channels and snapshot data participate in a stable revision/ETag path.

These observations are not a substitute for rerunning tests in the new window.

### AETHER

Fresh `git status` confirms the integration worktree is also heavily dirty. Important additions include:

- `crates/aether-relay-core` with dynamic `quota_per_unit` algorithms and collaboration tests;
- `apps/aether-gateway/src/relay/` modules;
- Gateway startup invocation of `configure_relay_engine`;
- relay HMAC verification and atomic replay protection in the real proxy entry;
- verified relay headers are stripped and a trusted internal extension is inserted;
- New API event consumer with database inbox, opaque cursor persistence, and idempotency;
- price/balance/event background-worker registration;
- relay event migrations and repository code for SQLite, MySQL, and PostgreSQL;
- shared contract files and superseded Kiro SPEC markers.

Previously verified in the earlier window:

```text
cargo test -p aether-gateway ... --no-run     exit 0
startup enabled/disabled tests                2/2
relay::collaboration tests                    5/5
proxy relay collaboration tests               2/2
event_consumer tests                          3/3
cargo test -p aether-data relay_events        2/2
sqlite relay migration test                   1/1
cargo fmt --all -- --check                    exit 0
```

Treat these as earlier evidence only. Rerun them after restoring full workspace permissions.

## 4. Current cross-contract state

The following three files are byte-identical across both repositories as of this handoff:

```text
docs/contracts/aether-newapi-v1.json
docs/contracts/aether-newapi-v1.schema.json
docs/contracts/aether-newapi-v1.examples.json
```

Current per-file SHA-256 values in both repositories:

```text
aether-newapi-v1.json          E59202E21425BCE335B9CF2BEAE940CAEF6717A570261D614FC5FA9EADE62273
aether-newapi-v1.schema.json   92954115315BBDE2DE625EC8ACDF319FF773407074BED88F3105C85E8D279C67
aether-newapi-v1.examples.json 82DEBEDD68FFD5477BEB33EDA9AE9A815C6FDA054A5B1182A5DA1EEA2C688B39
```

The schema now requires `group`, `model`, and `relay_format` in the relay context. Examples now contain both:

- `relay_signature_vector`
- `service_signature_vector` with Unicode/reserved-query canonicalization

### Immediate known red condition

The bundle hash in both manifests is stale:

```text
manifest bundle_sha256: 6ad9c1e5cffa66cff1212effa1db7ca04cb4003093c73912c176dfda35ddc730
actual SHA256(schema + NUL + examples): bbeb8fbd2091b4001f43d02195ce38aa215e8f044ed6eaa3d42b718fd464a4bb
```

First update `bundle_sha256` to the actual value in both repositories, then run the Go and Rust contract tests. Do not update only one repository.

## 5. Highest-priority incomplete work

### New API P0/P1

1. Rerun all new outbox transaction tests. Confirm the business mutation and outbox insert share the same main-database transaction.
2. Trace usage settlement end to end. Main-DB outbox is authoritative; a separate `LOG_DB` cannot be claimed as cross-database atomic.
3. Verify `BillingSession.Refund`, task refund, consume log, top-up, subscription, channel batch operations, and pricing option paths after the latest edits.
4. Run the pricing/snapshot tests after fixing the contract hash. Confirm full-window totals and ETag behavior across pages.
5. Complete frontend connection/health/sync/conflict UI and i18n only after backend contracts are stable.

### AETHER P0/P1

1. `TrustedRelayContext` is currently inserted only in `relay/collaboration.rs` and its tests. It is not consumed by the real route/provider/usage-settlement pipeline. Add TDD coverage for model/format mismatch, trusted request ID propagation, anonymous group context, API-key auth preservation, and exactly one real upstream call.
2. `export_api.rs` still returns placeholder empty pricing/usage/events data.
3. `event_outbox.rs` still writes RuntimeState and `query_events` returns empty. Database must be the truth source.
4. `integrations_api.rs` still uses non-atomic RuntimeState `kv_get` + `kv_set` for revision changes. Replace with database compare-and-set and return 409 with the current config/diff.
5. The profit receiver created in `state/core.rs` is discarded (`_profit_receiver`), so profit persistence is not running.
6. Provider/channel runtime still expands the `relay_channels` side path. Migrate execution to the existing Provider Catalog, Provider API Keys, Pool, Routing Profile, Quota Snapshot, and Usage Settlement domains.
7. Production hardcoding remains in `relay/reconcile.rs` around line 152. Replace it with dynamic `quota_per_unit` or `unknown`.
8. Reserved modes are still advertised/configurable in places. They must not be activatable before their full gates exist.
9. AETHER admin Provider/Pool/Routing Profile UI and the known token/cache accounting failures remain unfinished.

## 6. Environment facts

No `cargo`, `rustc`, or `go` process was running at handoff time.

The restricted window could read both repositories but could write only New API. Go tests failed before compilation because the auto-downloaded Go toolchain could not create this lock file:

```text
D:\DevCache\go-mod\cache\download\golang.org\toolchain\@v\v0.0.1-go1.25.1.windows-amd64.lock
```

The new window must grant write access to `D:\DevCache\go-mod` or use an already installed compatible Go toolchain/cache.

Earlier AETHER compilation used these local tools:

```text
CMake:        D:\DevCache\codex-tools\cmake\cmake\data\bin
NASM:         D:\DevCache\codex-tools\nasm\nasm-3.02
LIBCLANG_PATH D:\DevCache\codex-tools\libclang\clang\native
```

## 7. First commands in the new window

Run these only after opening a workspace with write permission for both repositories and cache/tool directories.

```powershell
git -C D:\workspaces\new-api status --short --branch
git -C D:\workspaces\Aether-pureai status --short --branch
```

After fixing the bundle hash in both repositories:

```powershell
go test ./model -run TestAetherLedgerEventTxFunctionsFollowBusinessTransaction -count=1
go test ./service -run TestAetherContractManifestDefinesCanonicalV1Surface -count=1
go test ./controller -run 'Test(RelayPricingContract|GetAether)' -count=1
go test ./controller ./service ./model ./middleware -count=1
```

Run from `D:\workspaces\Aether-pureai`:

```powershell
cargo test -p aether-relay-core
cargo test -p aether-data relay_events
cargo test -p aether-gateway --no-run
cargo fmt --all -- --check
```

Do not run duplicate Cargo builds. Check for existing `cargo`/`rustc` processes first.

## 8. Completion rule

Do not mark the goal complete while any of these remain unproven: main-DB outbox atomicity, real AETHER Provider/Pool/SSE/retry/settlement integration, database truth for AETHER integrations/events/profit, dynamic pricing/balance routing, key rotation, three-database behavior, both management UIs, full builds, or the dual-process exactly-once E2E exercise.

