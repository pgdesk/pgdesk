# Changelog

pgdesk follows [semantic versioning](https://semver.org). Until v1, minor
versions may break the API.

## Unreleased

- pgdesk's own code no longer needs anything newer than Go 1.22. The module
  still requires Go 1.25, because pgx v5.9.2 and golang.org/x/text v0.39.0
  (the first releases with fixes for GO-2026-5004 and GO-2026-5970) do.

## v0.1.1 (2026-09-29)

- `IncludeViews` did nothing: views were dropped for having no primary key.
  They are now registered as list-only resources.

## v0.1.0 (2026-09-29)

First release.

- Mount an admin panel on any `http.Handler` router. It reads tables, columns,
  foreign keys and CHECK constraints from `pg_catalog`.
- Lists with search, filters, sorting, pagination and CSV export. Search and
  labels follow foreign keys.
- Create, edit and delete forms built from column types and constraints, with
  optimistic locking and bulk actions.
- Bring your own authentication. `Authorizer` controls what each user may do
  and `Scoper` limits which rows they see.
- Audit log written in the same transaction as the change. CSRF protection
  and a strict CSP.
