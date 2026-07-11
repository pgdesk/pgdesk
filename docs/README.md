# pgdesk documentation

- **[GUIDE.md](GUIDE.md)** -- practical how-to: resources, bulk actions, CSV export,
  audit, authorization & row scoping, auto-registration, UI customization, and
  production operation (health, reload, timeouts, metrics).
- **[AUTHORIZATION.md](AUTHORIZATION.md)** -- the authorization & row-scoping design in
  depth: how it fixes pagination, concurrency, and bulk-action safety.
- **[ARCHITECTURE.md](ARCHITECTURE.md)** -- the full design and implementation log of
  the load-bearing architectural decisions.

Runnable examples live in [`../examples`](../examples): [`basic`](../examples/basic)
(minimal single-file app), [`session-auth`](../examples/session-auth)
(signed-cookie `Principal` wiring), [`gorm`](../examples/gorm) (pgdesk over a
GORM-owned schema, sharing one pool), and [`gin`](../examples/gin) (mounted behind
a Gin router).

New here? Start with the [README](../README.md), then the [GUIDE](GUIDE.md).
