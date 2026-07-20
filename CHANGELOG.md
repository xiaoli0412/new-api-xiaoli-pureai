# Changelog

All notable changes to this project are documented in this file.

## [1.0.0-rc.22] - 2026-07-20

### Added

- Added administrator-managed AETHER channel integration with capability, configuration, and status synchronization, revision-conflict feedback, and credential rotation.
- Added a short-lived HMAC-signed pseudonymous relay context for AETHER forwarding. The context carries routing metadata only and excludes user API keys, identity data, payment credentials, and balance details.
- Added authenticated, read-only `aether-newapi/v1` pricing, event, and snapshot contracts, plus transaction-aware, deduplicated AETHER ledger events for usage, financial, subscription, channel, balance-observation, and pricing changes.
- Added AETHER channel integration controls and localized interface text in the default web console.

### Security

- Separated control-plane and relay-signing credentials, stored the configured credentials encrypted, and added a bounded dual-secret transition for rotation.
- Restricted real forwarding to `direct_channel`; `parallel_shadow` and `aether_decision` fail closed in this prerelease.

### Compatibility

- New API remains the exclusive authority for users, balances, top-ups, refunds, subscriptions, settlement, and user pricing. AETHER receives anonymized, read-only integration data and cannot automatically write prices back to New API.
- This is a prerelease. The shared operator contract is available at `docs/contracts/aether-newapi-v1.json`.
