# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

**Nothing has been released yet.** There are no tags, so every entry below is
unreleased and the API may still change. Breaking changes are called out as such.

## [Unreleased]

### Security

- `CapAccessAdmin` now gates every admin route, not only the index page.
  Previously an authorizer that denied it -- the most natural way to express "this
  operator is not an admin" -- hid the navigation while every resource route still
  served rows: `/admin/` returned 403 but `/admin/users`, its detail, edit, export
  and picker routes all returned 200 with data. Static assets stay reachable so
  the 403 page renders.
- A submitted foreign-key value is now validated against the referenced
  resource's row scope, inside the mutation's own transaction. The picker offers
  only in-scope rows, but the write path accepted any key an operator typed, so a
  hand-written POST could create a reference across a scope boundary -- and the
  accept/reject answer was an oracle for which keys exist in a table the operator
  cannot read. A violation renders the same inline 422 field error a database
  validation failure does. Foreign keys to unregistered tables, and referenced
  resources with no scope, are unaffected.

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

- Foreign-key pickers. A single-column foreign key whose referenced table is a
  registered resource now renders as a bounded search combobox on create/edit
  forms, and resolves to a linked label on the detail page (previously only the
  list page did this; forms and detail showed the raw key).
  - New endpoint `GET {basePath}/{resource}/options.json?q=` returns at most
    `WithMaxOptions(n)` (default 20) `{value,label}` pairs, and reports
    `truncated`. It requires `CapList` on the referenced resource and applies
    that resource's row scope, so a picker cannot reveal a row the operator
    could not have listed. It answers in JSON on every path, including refusals.
  - New option `WithMaxOptions(n)`; new `Widget` constant `WidgetFK`. An explicit
    `Resource.Widget` still wins over the foreign-key default.
  - The response also reports `searchable`, so a resource with no text column to
    match says the term was not applied instead of returning the unfiltered first
    page as if it were a set of matches.
  - A readonly foreign key (generated, identity, or `Resource.Readonly`) shows its
    label too, matching the detail page.
  - The field stays a real `<input>` carrying the key, so forms keep working with
    JavaScript disabled; `pgdesk.js` upgrades it in place to a WAI-ARIA combobox.
- Documentation: Authorization & row scoping section with concrete tenant-scoping example
- Documentation: Production checklist covering readiness probes, catalog reloads, metrics, timeouts, and audit
- New example: `examples/session-auth/` demonstrating Principal attachment via signed cookie middleware

### Initial feature set (2025-07)

When the v1 surface came together. Never tagged or published -- breaking changes
landed afterwards (`WithVersionColumn` -> `VersionColumn`, `WithConstraintMessage`
-> `ConstraintMessage`, `Resource.Authorize` and `WithActionAllowed` removed,
`admin.Resource` -> `WithResource`), which is why this is not a release heading.

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
