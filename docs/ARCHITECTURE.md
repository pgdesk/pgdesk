# pgdesk -- Architecture Note

`pgdesk` is a PostgreSQL-first, pgx-first admin framework for Go. It gives
developers Django-Admin-like leverage for internal tools: schema introspection,
declarative resource config in Go, a server-rendered HTML-first UI, and
fail-closed safe defaults for internal production use.

This note records the load-bearing decisions. They came out of an architecture
review and are **closed**; the code implements them as written. Each decision is
a concrete, testable guarantee, not an aspiration.

## Design invariants at a glance

| ID | Guarantee | Primary home |
|----|-----------|--------------|
| D1 | Immutable catalog behind an atomic pointer; RepeatableRead load; `Reload` hot-swaps | `internal/introspect`, `admin.go` |
| D2 | Fail-closed exposure: zero resources until named; opt-in auto-register logs the set | `resource.go`, `options.go` |
| D3 | SQL safety: identifiers resolve to catalog objects; only values are `$N` | `internal/query` |
| D4 | FK labels via batched `= ANY($1)` lookups, never generated JOINs | `internal/query` |
| D5 | CSRF: signed double-submit, `__Host-` cookie, key required at boot | `internal/csrf` |
| D6 | Keys: composite/uuid/text-aware; capability derived from key presence | `internal/introspect`, `internal/query` |
| D7 | PostgreSQL is the validator; `*pgconn.PgError` mapped by SQLSTATE | `internal/query`, `handler.go` |
| F1 | All DB/request content is untrusted; escaping banned-bypass discipline | `internal/render` |
| F2 | Strict nonce-based CSP | `handler.go` |
| F3 | Security-headers middleware on by default | `handler.go` |
| F4 | No open redirects: relative-under-base-path only | `handler.go` |
| F5 | Templates compiled once; fail-closed; no internal leakage to browser | `internal/render` |
| F6 | `MaxBytesReader`, clamped page size, capped bulk selection | `handler.go`, `internal/query` |
| F7 | Async/JSON endpoints inherit every backend contract | `handler.go` |
| F8 | Static assets from `embed.FS`, immutable cache, no traversal | `handler.go`, `assets/` |
| O1 | No lost updates: optimistic concurrency via `xmin` (or version column) | `internal/query` |
| O2 | Bounded everything: per-request timeout + `statement_timeout` backstop | `admin.go`, `options.go` |
| O3 | Clean lifecycle: `Close`, `Healthy`; host owns the pool | `admin.go`, `health.go` |
| O4 | Durable audit: mutation + audit atomic in one tx | `audit.go`, `internal/query` |
| O5 | Observability: `slog`, request IDs, optional metrics hook | `observability.go`, `context.go` |
| O6 | Authorization enforced centrally, fail-closed | `auth.go`, `handler.go` |
| O7 | Testing rigor: `-race`, fuzz, real-Postgres integration | `*_test.go` |
| O8 | Supply-chain: vet/staticcheck/govulncheck/gofmt gates, SemVer | `.github/workflows` |

## The SQL-safety invariant (D3), stated precisely

**No request-supplied string is ever concatenated into SQL.**

- **Values** become `$N` placeholders through an append-only arg builder
  (`query.Args`). The builder owns the numbering; callers never format `$N`
  themselves.
- **Identifiers** (columns used for sort/filter/search/select) are never
  interpolated from the request. The request string is used *only as a map key*
  to resolve to a `*introspect.Column` whose `Name` came from `pg_catalog`.
  Unresolved -> `400`, fail closed.
- Resolved identifiers are quoted at emit time as defense-in-depth:
  `ident(s) = "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""`.
- **Filters** use a typed operator whitelist keyed on the column's Postgres type
  category. An operator token that does not resolve for that category is
  rejected. Enum values are validated against catalog enum labels.

This subsystem carries the heaviest test suite (table-driven, every category x
operator, adversarial inputs) and is fuzzed (O7): the asserted invariant is
"resolves to a catalog object or is rejected -- never emitted as raw SQL."

## Request lifecycle

1. Recover middleware (panic -> logged 500, F5).
2. Security headers + per-request CSP nonce minted (F2, F3).
3. Request ID resolved (honor `X-Request-Id` else generate) and a request-scoped
   `slog.Logger` bound into the context (O5).
4. **Catalog snapshot loaded once** from the atomic pointer (D1) and carried in
   context -- a mid-request `Reload` cannot shift schema underneath the handler.
5. Principal resolved from context (host middleware put it there); the matching
   `Authorizer` hook is called **before any query** (O6), fail-closed.
6. Route resolves a `*Resource`, decodes the key type-aware (D6), builds
   parameterized SQL (D3), runs it under a per-request deadline (O2).
7. Mutations run in a single transaction with `RETURNING`; audit writes in the
   same tx (O4); `xmin`/version guards against lost updates (O1); `PgError`
   mapped to inline field errors (D7).
8. Render through templates compiled at `New()` (F5); all dynamic values escaped
   (F1).

## Ownership boundaries

- The host owns the `*pgxpool.Pool` lifecycle. `pgdesk` never closes it.
- The host owns the `*http.Server`, TLS, network isolation, rate limiting, WAF,
  and session/auth mechanics. `pgdesk` provides clean contracts: `Principal`,
  `Middleware`, `Authorizer`, `AuditLogger`, `Metrics`.
- `pgdesk` owns the catalog, routing, templates, CSRF cookie, and its own
  per-request deadlines.

## Scope boundary

`pgdesk` is hardened for a **trusted-operator internal tool**. It defends against
stored XSS from DB content, CSRF, clickjacking, open redirects, SQL injection,
lost updates, resource exhaustion, and audit gaps. It intentionally does **not**
defend against a malicious authenticated operator (they have legitimate DB access
by design), and it **delegates** TLS, network isolation, rate limiting, WAF, and
auth/session mechanics to the host. Do not deploy it raw to the public internet.

## Dependency surface

Runtime deps: `github.com/jackc/pgx/v5` (+ its own deps) and the standard
library. No web framework, no ORM, no template engine beyond `html/template`, no
logging framework beyond `log/slog`. Every additional module is a supply-chain
liability and must be justified here before being added.
