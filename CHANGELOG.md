# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- Fixed documentation: bulk action example now uses correct `pgdesk.Keys` API instead of `[][]any`, with explanation of typed accessors and PgBouncer safety (#17)
- Fixed `AuditEvent` doc comment to reference `Resource.Redact()` instead of non-existent `Field.Redact` (#37)
- Improved adoption-review: resource identity encoding, scope enforcement, version token handling, export safety, and error handling hardening
- Fixed dead documentation links (CHANGELOG.md and TASKS.md references)

### Added

- Documentation: Authorization & row scoping section with concrete tenant-scoping example
- Documentation: Production checklist covering readiness probes, catalog reloads, metrics, timeouts, and audit
- New example: `examples/session-auth/` demonstrating Principal attachment via signed cookie middleware

## [1.0.0] - 2025-07

First stable release. Features:

- Schema introspection with live catalog snapshots
- Declarative resource configuration
- Full CRUD with optimistic concurrency
- Bulk actions and CSV export
- Durable audit logging (transactional and out-of-band)
- Authorization with row scoping
- CSRF protection and security hardening
- Server-rendered HTML UI with dark mode and keyboard navigation
- Auto-registration with schema filtering
- Extensive configuration options (timeouts, metrics, middleware, etc.)
