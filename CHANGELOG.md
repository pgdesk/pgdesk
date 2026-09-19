# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- `numeric`, `uuid`, `json`/`jsonb`, `time`, `interval`, `bit` and `point` values
  now render as their PostgreSQL text form instead of a Go struct dump
  (`{1050 -2 false finite true}`, `[72 63 80 ...]`, `map[a:1]`). This affected
  every surface -- list, detail, CSV export and the edit form -- and because the
  edit form pre-fills from the same value, a row with a `numeric` or `uuid` column
  could not be saved at all: resubmitting the pre-filled value was rejected with
  422 "One of the values has an invalid format."
- `GENERATED ALWAYS AS IDENTITY` columns no longer render as editable, required
  form inputs. The write path already dropped them, so anything typed there was
  silently discarded. A readonly field is no longer marked required either.
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
