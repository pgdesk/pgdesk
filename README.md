# pgdesk

**A production admin panel for your PostgreSQL database -- in one function call.**

pgdesk reads your schema and gives you a complete, server-rendered admin UI --
list, search, filter, sort, CRUD, bulk actions, CSV export, and a durable audit
trail -- with Django-Admin-like leverage and Go's type safety. No ORM, no Node
build, no framework lock-in. Point it at a `pgxpool`, declare what to expose,
mount it on any `http.ServeMux`.

> Usable in minutes, extensible in weeks.

```go
admin, err := pgdesk.New(pool,
    pgdesk.WithTitle("Operations Admin"),
    pgdesk.WithSecretKey(secret),
    pgdesk.WithAuthorizer(policy),
    pgdesk.WithResource("users", func(r *pgdesk.Resource) {
        r.ListDisplay("id", "email", "status", "created_at")
        r.SearchFields("email", "full_name")
        r.Filters("status", "created_at")
        r.DefaultSort("-created_at")
    }),
)
if err != nil {
    log.Fatal(err) // misconfiguration is an error, never a panic
}

mux := http.NewServeMux()
admin.Mount(mux) // that's it -- the admin is live at /admin/
```

## Why pgdesk

- **PostgreSQL-first, pgx-first.** It reads `pg_catalog` to understand your schema
  and speaks pgx natively -- including capabilities you can't fake: a view that
  isn't updatable gets no edit button, an identity column is dropped from the
  insert form. Not database-agnostic, and better for it.
- **ORM-agnostic.** pgdesk reads your database, not your models -- so it drops in
  next to GORM, ent, sqlc, bun, or hand-written SQL with no adapter and no
  lock-in. Share one `pgxpool` and your existing schema is administered as-is.
  ([GORM example ->](examples/gorm))
- **Standard library, all the way down.** `net/http`, `html/template`,
  `context`. Mounts on a `ServeMux` or acts as a bare `http.Handler`. No Chi, Gin,
  Echo, or Fiber. No mandatory Node build.
- **One runtime dependency.** `github.com/jackc/pgx/v5` and the standard library.
  That's the entire tree.
- **Safe by default, fail-closed.** Nothing is exposed until you name it. Every
  capability is denied until you authorize it. No request string ever reaches SQL
  as anything but `$N`. No lost updates under concurrent edits. Mutations can't
  commit without their audit record.
- **Authorization that composes.** An `Authorizer` answers *may I?*; an optional
  `Scoper` answers *which rows are mine?* -- pushed straight into the `WHERE`
  clause, so row scoping is atomic and paginates correctly. Rules combine under a
  deny-overrides algebra that can only ever narrow access, never widen it.
- **No fake stubs.** Every shipped feature is exercised by a real-PostgreSQL
  integration suite, run under `-race`.

## What you get

CRUD with optimistic concurrency | typed filters, ILIKE search, sortable columns |
foreign-key labels with per-resource overrides | transactional bulk actions |
streaming CSV export through the same authorizer | durable in-transaction audit |
CSRF, nonce'd CSP, clickjacking & open-redirect defenses | readiness probe, hot
catalog reload, metrics hook | flash messages, dark mode, keyboard shortcuts |
opt-in auto-registration that logs its full exposed set at startup.

-> See the **[usage guide](docs/GUIDE.md)** for how to wire all of it, and the
**[examples](examples)** for runnable apps: [basic](examples/basic),
[session-auth](examples/session-auth), [gorm](examples/gorm) (pgdesk over a
GORM-owned schema, sharing one pool), and [gin](examples/gin) (mounted behind a
Gin router).

## Security & scope

pgdesk is hardened for a **trusted-operator internal tool**. It defends against
stored XSS from DB content, CSRF, clickjacking, open redirects, SQL injection,
lost updates, resource exhaustion, and audit gaps.

It intentionally does **not** defend against a malicious authenticated operator
(they have legitimate DB access by design), and it **delegates** TLS, network
isolation, rate limiting, WAF, and session/auth to the host -- providing clean
contracts (`Principal`, `Middleware`, `Authorizer`, `Metrics`) for each. Don't
deploy it raw to the public internet expecting more than it claims.

## Documentation

- **[Usage guide](docs/GUIDE.md)** -- resources, actions, export, audit,
  authorization, UI customization, and production operation.
- **[Authorization design](docs/AUTHORIZATION.md)** -- row scoping, pagination,
  concurrency, and bulk-action safety.
- **[Architecture](docs/ARCHITECTURE.md)** -- the full design-decision log.

## Requirements

- Go 1.25+ (floor set by pgx v5.10)
- PostgreSQL 12+

pgdesk follows [SemVer](https://semver.org/); pre-1.0, breaking changes are called
out in [CHANGELOG.md](CHANGELOG.md). **v1 feature-complete**, pre-1.0 API polish ongoing.

## License

MIT -- see [LICENSE](LICENSE).
