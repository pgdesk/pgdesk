# pgdesk — Authorization Design

Status: **implemented**, superseding the seven-method `Authorizer`.
Pre-1.0, zero users; the interface broke now rather than never.

This note records the authorization model and, as importantly, the alternatives
that were rejected and why. It follows the house style of `ARCHITECTURE.md`:
every guarantee below is concrete and testable, not an aspiration.

---

## 1. The two questions

Authorization in an admin panel is two different questions, and conflating them
is what produced the defects this design fixes.

| Question | Shape | Answered by |
|----------|-------|-------------|
| *May I perform capability C on resource R?* | one decision | `Authorizer` |
| *Which rows of R exist for me?* | a set | `Scoper` |

The first is a boolean gate on a route. The second is a predicate on a query.
Trying to answer the second with the first — a per-row Go callback — breaks
pagination (a `LIMIT n+1` lookahead post-filtered in Go yields short pages and a
wrong `HasNext`) and cannot be made atomic against a concurrent write.

Answering it where it belongs, in the `WHERE` clause, is simultaneously the
simpler, the faster, and the only correct option. That is the central idea here.

## 2. Design rules

- **Simplicity lives in the interface; extensibility lives in the data type.**
  One method that never changes; a struct that grows by adding fields.
- **Fail closed by construction, not by convention.** The zero value of
  `Decision` is `Deny`. A zero-valued, partially-written, or forgotten decision
  denies without anyone remembering to make it so.
- **No I/O on the decision path.** `Authorize` is a pure function of its
  arguments for every authorizer pgdesk ships. The invariant from `auth.go` —
  *the capability is checked before any query runs* — is restored unconditionally.
- **Predicates belong in SQL.** Row-level authorization is a `WHERE` fragment,
  not a callback. This is what makes it both fast and atomic.
- **Optional interfaces over fat ones.** Row scoping is opt-in via a type
  assertion, the way `io.Copy` looks for `io.ReaderFrom` and `net/http` looks for
  `http.Flusher`. An authorizer that does not scope pays nothing and implements
  one method.

## 3. The API

```go
// Principal is the authenticated operator for a request. Unchanged.
type Principal interface {
	SubjectID() string
	DisplayName() string
}

// Capability enumerates the authorization checks pgdesk performs. Exactly one
// capability is checked per route, in the handler layer, before any query runs.
type Capability string

const (
	CapAccessAdmin Capability = "access_admin"
	CapList        Capability = "list"
	CapView        Capability = "view"
	CapCreate      Capability = "create"
	CapUpdate      Capability = "update"
	CapDelete      Capability = "delete"
	CapRunAction   Capability = "run_action"
)

// Decision is the outcome of one authorizer's check.
//
// The zero value is Deny. An uninitialized Decision, a Decision returned
// alongside an error, and a Decision decoded from a missing field all forbid the
// operation. Fail-closed is a property of the type, not of the caller.
type Decision int

const (
	Deny    Decision = iota // this authorizer forbids the operation
	Allow                   // this authorizer permits the operation
	Abstain                 // this authorizer has no opinion; others decide
)

// Attributes describes the operation under consideration. It is a value type, so
// pgdesk can add fields in a later release without breaking implementations.
//
// It is deliberately not named Request: every call site in this package already
// has an *http.Request named r in scope.
type Attributes struct {
	Principal  Principal
	Capability Capability
	Resource   string // resource name; empty for CapAccessAdmin
	Action     string // action name; set only for CapRunAction
}

// Authorizer decides whether a principal may perform a capability.
//
// pgdesk consults it at one choke point per route, before any query runs, and
// again when rendering an affordance, so the UI never offers an operation the
// operator cannot perform. Both call sites reduce the Decision the same way:
// only Allow allows.
//
// Authorize must be safe for concurrent use. It is called once per rendered
// action on a list page, so it should be cheap; an implementation that performs
// I/O is responsible for memoizing it against the request context.
type Authorizer interface {
	Authorize(ctx context.Context, attrs Attributes) (Decision, error)
}

// AuthorizerFunc adapts an ordinary function to Authorizer.
type AuthorizerFunc func(ctx context.Context, attrs Attributes) (Decision, error)

func (f AuthorizerFunc) Authorize(ctx context.Context, attrs Attributes) (Decision, error) {
	return f(ctx, attrs)
}

// AllowAll is an Authorizer value that permits every capability and restricts no
// rows — a convenience for hosts that gate the whole admin behind their own
// middleware. It is a value, not a type, following io.Discard and
// slog.DiscardHandler: WithAuthorizer(pgdesk.AllowAll).
var AllowAll Authorizer = allowAll{}
```

### 3.1 Composition

```go
// DenyOverrides combines authorizers with the deny-overrides rule: any Deny
// forbids the operation; otherwise a single Allow permits it; otherwise the
// result is Abstain. An authorizer that returns an error denies, and the error
// propagates.
//
// Because Abstain is the identity of this operation, adding an authorizer to a
// DenyOverrides set can only ever narrow access, never widen it. Because the
// result is Abstain rather than Deny when every member abstains, the sets nest.
//
// Nil authorizers are dropped. DenyOverrides() abstains, which denies.
func DenyOverrides(azs ...Authorizer) Authorizer {
	set := make(denyOverrides, 0, len(azs))
	for _, az := range azs {
		if az != nil {
			set = append(set, az)
		}
	}
	return set
}

type denyOverrides []Authorizer

func (set denyOverrides) Authorize(ctx context.Context, attrs Attributes) (Decision, error) {
	allowed := false
	for _, az := range set {
		dec, err := az.Authorize(ctx, attrs)
		if err != nil {
			return Deny, err
		}
		switch dec {
		case Deny:
			return Deny, nil
		case Allow:
			allowed = true
		}
	}
	if allowed {
		return Allow, nil
	}
	return Abstain, nil
}
```

The single reduction from `Decision` to `bool` lives in exactly one unexported
function (`permitted`, in `auth.go`). It is written `dec == Allow`, never
`dec != Deny`, and that distinction is the difference between fail-closed and
fail-open:

```go
// permitted reports whether az allows attrs. Every path that is not an explicit
// Allow denies: a nil authorizer, a nil principal, an Abstain, or an error.
func permitted(ctx context.Context, az Authorizer, attrs Attributes) (bool, error) {
	if az == nil || attrs.Principal == nil {
		return false, nil
	}
	dec, err := az.Authorize(ctx, attrs)
	if err != nil {
		return false, err
	}
	return dec == Allow, nil
}
```

### 3.2 Row scoping

```go
// Constraint is one predicate ANDed into every statement pgdesk issues for a
// resource. Build one with Eq, Ne, or In — there is no other constructor, so a
// Constraint is always well-formed. The column is resolved against the live
// catalog snapshot (D3) and emitted as a quoted identifier; the value becomes $N.
// An unknown column, or an operator the column's type category forbids, denies
// the request rather than dropping the predicate.
type Constraint struct{ /* opaque */ }

func Eq(column string, value any) Constraint    // column = value
func Ne(column string, value any) Constraint    // column <> value (SQL: excludes NULL)
func In(column string, values ...any) Constraint // column IN (values...)

// ScopeOnly lifts a scope function into an Authorizer that abstains on every
// capability and only narrows rows — for a rule that restricts which rows a
// principal may reach but makes no permission decision. fn may be a bare function
// or a method value, so a stateful scoper needs no wrapper.
func ScopeOnly(fn func(ctx context.Context, attrs Attributes) ([]Constraint, error)) Authorizer

// Scoper narrows the set of rows a principal may reach. It is an optional
// interface: when an Authorizer also implements Scoper, pgdesk ANDs the returned
// constraints into the WHERE clause of every statement it issues for that
// resource.
//
// Scope answers "which rows exist for me"; Authorize answers "may I". Keeping
// them apart is what makes list pagination correct — the constraint is in the
// query, so LIMIT applies after filtering — and what makes row-level
// authorization atomic: the predicate rides in the mutation's own WHERE clause,
// leaving no window between the check and the write.
//
// Scope takes the same Attributes as Authorize, so the constraint may depend on
// the capability: an operator can be permitted to see a row on the list page and
// still be forbidden to update or delete it. pgdesk calls Scope with the
// capability of the statement it is about to build — CapList for the list and
// export, CapView for the detail page and FK labels, CapUpdate for the edit form
// and the UPDATE, CapDelete for the DELETE, CapRunAction for a bulk action's key
// selection.
//
// A nil slice means "no restriction" and costs nothing.
//
// An Eq constraint is additionally pinned on INSERT and UPDATE: the scoped column
// is written from the constraint's value, ignoring anything the operator
// submitted, so a scoped operator can neither create a row outside their scope nor
// move one out of it. Ne and In have no single value to pin and constrain only the
// WHERE side; mark such columns Readonly if the operator must not set them.
type Scoper interface {
	Scope(ctx context.Context, attrs Attributes) ([]Constraint, error)
}

// Scope implements Scoper for a DenyOverrides set by concatenating the
// constraints of every member that is itself a Scoper. Constraints compose by
// conjunction, so — as with Deny — adding an authorizer can only narrow the
// rows a principal can reach.
func (set denyOverrides) Scope(ctx context.Context, attrs Attributes) ([]Constraint, error)
```

## 4. Where the scope is applied

A scope is a `WHERE` fragment. It must be ANDed into **every** statement that
names a row, or the constraint is decorative. The exhaustive list:

| Statement | Callers | Effect of the scope |
|-----------|---------|---------------------|
| `BuildList` | list, CSV export | Rows outside scope never appear. `LIMIT n+1` lookahead stays correct. |
| `SelectRow` | detail, edit form | Out-of-scope row → 0 rows → **404**, never 403. |
| `UpdateRow` | update | Predicate rides in the mutation's `WHERE`. Atomic. |
| `DeleteRow` | delete | Same. Closes the delete TOCTOU, which no version token can. |
| `BuildFKLabels` | FK label resolution | Uses the *referenced* resource's scope. |
| host action `fn` | bulk actions | Keys are pre-filtered; see below. |

Three consequences are worth stating explicitly, because each fixes a live bug.

**404, not 403.** An out-of-scope row is indistinguishable from a nonexistent
one. The response cannot be used to enumerate rows.

**A scoped mutation affecting zero rows is ambiguous** — the row may have moved
under us (the O1 lost-update guard) or it may simply not be ours. Today, zero
rows unconditionally means `errConflict`, which renders as *"someone else changed
this row, reload"*. Under a scope that would tell a permanently forbidden
operator to keep retrying. We pay for the distinction only on the failure path:

```go
// A scoped UPDATE or DELETE that affects no rows is ambiguous. Probe for the row
// within scope, ignoring the version token: found means a genuine optimistic
// concurrency conflict (409); not found means the row does not exist for this
// principal (404). The probe runs only when the mutation already failed.
```

**Bulk actions run host SQL that pgdesk cannot rewrite** (`act.fn(ctx, tx, keys)`).
The keys are therefore filtered before the action sees them: one scoped
`SELECT key ... WHERE key = ANY($1) AND <scope>` returns the subset the principal
may reach, and only that subset is handed to the action. Without this, a bulk
action is an unbounded IDOR — the capability check is per-resource, and the
action receives whatever keys the form posted.

**FK label resolution consults the referenced resource.** Today
`resolveFKLabels` reads the referenced table's label column for every foreign key
on the page, whether or not that table is a registered resource, and with no
authorizer involved. A `users` list leaks `employees.full_name` even when the
`employees` resource is restricted. The rule becomes: resolve labels only for
referenced tables that are registered resources for which the principal holds
`CapView`, and AND that resource's scope into the lookup. This is also a
simplification — the current `refRegistered` branch, which gates only the *link*
and never the label, goes away.

## 5. Example use

The host's principal, and a helper to reach its concrete type — `Principal` is
minimal by design, so the host asserts to its own type:

```go
type operator struct {
	ID      string
	OrgID   string
	IsAdmin bool
}

func (o operator) SubjectID() string   { return o.ID }
func (o operator) DisplayName() string { return o.ID }

func op(p pgdesk.Principal) operator { o, _ := p.(operator); return o }
```

### 5.1 Everyone reads, only admins write

```go
var adminWrites = pgdesk.AuthorizerFunc(func(_ context.Context, attrs pgdesk.Attributes) (pgdesk.Decision, error) {
	switch attrs.Capability {
	case pgdesk.CapAccessAdmin, pgdesk.CapList, pgdesk.CapView:
		return pgdesk.Allow, nil
	case pgdesk.CapCreate, pgdesk.CapUpdate, pgdesk.CapDelete, pgdesk.CapRunAction:
		if op(attrs.Principal).IsAdmin {
			return pgdesk.Allow, nil
		}
		return pgdesk.Deny, nil
	default:
		// A capability this policy has not been taught. Abstain, so a capability
		// added by a future pgdesk release is denied until we opt in.
		return pgdesk.Abstain, nil
	}
})
```

That `default: return Abstain` is the whole extensibility story, and it is worth
being precise about what it buys and what it does not. Adding `CapExport` to
pgdesk no longer breaks the host's build — but nor does it silently grant, because
an all-abstain result denies. A host who writes `default: return Allow` has opted
out of that guarantee, knowingly. The old seven-method interface bought the same
safety with a compile error, which is louder but hostile; this buys it with a
default that fails in the safe direction.

### 5.2 An independent rule that only ever narrows

```go
// Posted ledger entries are immutable. This rule knows nothing about roles.
var frozenLedger = pgdesk.AuthorizerFunc(func(_ context.Context, attrs pgdesk.Attributes) (pgdesk.Decision, error) {
	if attrs.Resource == "ledger_entries" {
		switch attrs.Capability {
		case pgdesk.CapUpdate, pgdesk.CapDelete:
			return pgdesk.Deny, nil
		}
	}
	return pgdesk.Abstain, nil // not my department
})

pgdesk.WithAuthorizer(pgdesk.DenyOverrides(adminWrites, frozenLedger))
```

This is the entire argument for a tri-state `Decision`. With a boolean interface,
`frozenLedger` would have to return `true` for every request it does not care
about — meaning it *grants* — and now ordering matters, and an ordering mistake
widens access. With `Abstain` as the identity, you can append a tenth rule and be
certain, without reading it, that nobody gained a permission.

### 5.3 Row-level: tenancy and ownership

```go
// tenantScope confines every resource to the operator's organization. It decides
// nothing; it only narrows rows, so it is a plain function wrapped in ScopeOnly.
func tenantScope(_ context.Context, attrs pgdesk.Attributes) ([]pgdesk.Constraint, error) {
	return []pgdesk.Constraint{pgdesk.Eq("org_id", op(attrs.Principal).OrgID)}, nil
}

// ownPosts lets non-admins reach only the posts they authored.
func ownPosts(_ context.Context, attrs pgdesk.Attributes) ([]pgdesk.Constraint, error) {
	if attrs.Resource != "posts" || op(attrs.Principal).IsAdmin {
		return nil, nil
	}
	return []pgdesk.Constraint{pgdesk.Eq("author_id", attrs.Principal.SubjectID())}, nil
}

pgdesk.WithAuthorizer(pgdesk.DenyOverrides(
	adminWrites, frozenLedger,
	pgdesk.ScopeOnly(tenantScope), pgdesk.ScopeOnly(ownPosts)))
```

Note what is *absent*. There is no `map[string]any`, so there is no opportunity
to compare a Go `string` against the `[16]byte` that pgx returns for a `uuid`
column (`pgtype/uuid.go:292`) — a comparison that compiles, never panics, and is
always false. The value goes to `$N`; Postgres compares it to `author_id` using
the column's own type. Type coercion is the database's job, and this design gives
it back.

Note also that `ownPosts` fixes the **list page**. A per-row callback could only
ever have answered for a single row, leaving `/posts` and `/posts/export.csv`
showing everything.

### 5.4 What the SQL becomes

```sql
-- GET /admin/posts?q=hello&sort=-created_at   (non-admin, org 42, subject 7)
SELECT "id", "title", "author_id", "created_at" FROM "public"."posts"
WHERE ("title" ILIKE $1 OR "body" ILIKE $1)
  AND "org_id" = $2
  AND "author_id" = $3
ORDER BY "created_at" DESC LIMIT $4 OFFSET $5

-- POST /admin/posts/7    (update)
UPDATE "public"."posts" SET "title" = $1
WHERE "id" = $2 AND xmin = $3
  AND "org_id" = $4 AND "author_id" = $5
RETURNING "id", "title", "author_id", "created_at"
```

The ownership check is a clause in the statement that performs the write. There
is no interval during which it can be falsified.

## 6. Performance

- **A resource with no scope produces byte-identical SQL to today.** `Scope`
  returns `nil`; `conds` gains no entries; nothing allocates.
- **No query runs during authorization.** The pre-query invariant holds
  unconditionally, so a denied principal cannot force a database round trip.
- **Constraints are pushed down.** They land in the same `WHERE` as a user
  filter, use the same indexes, and `LIMIT` applies after them — so the `n+1`
  lookahead pagination (pgdesk issues no `count(*)`) stays correct and cheap.
- **Resolution is a map lookup per constraint** against the in-memory catalog
  snapshot (D1). The typical scope has one constraint.
- **`Attributes` is a small value**, passed by value, no allocation.
- **There is one authorizer per admin.** The per-resource override is gone (see
  §7), so the guard and the view model read a single `cfg.authorizer` field. No
  per-request composition, no allocation.
- **Extra queries are confined to failure and to actions**: the 409-vs-404 probe
  runs only after a mutation has already affected zero rows; the bulk-action key
  filter runs only when a scope exists.
- **Render-time cost is `1 + len(actions)` calls per list page.** In-process for
  any authorizer pgdesk ships. Authorizers doing I/O must memoize against the
  request context, and this is documented on the interface.

## 7. Rejected alternatives

**A lazy row loader on `Attributes`** — `Row func(context.Context) (map[string]any, error)`.
Rejected on four independent grounds. It hides I/O in a struct field of a value
type. It punctures the pre-query invariant, and a conditionally-held invariant is
not an invariant. It is inherently racy: the row it reads is not the row the
subsequent `UPDATE` touches, and `DeleteRow` carries no version token, so nothing
closes the window. And `map[string]any` hands hosts raw pgx values — `[16]byte`
for `uuid`, `pgtype.Numeric` for `numeric`, `pgtype.InfinityModifier` for an
infinite `timestamptz` — inside security predicates, where a wrong type
comparison is silent. The scope hook subsumes every use case the row loader was
meant to serve, and does so atomically.

**A `reason string` third return**, as `k8s.io/apiserver` has. Rejected: pgdesk
renders one generic 403 by design, so the framework would never consume it. An
authorizer that wants to explain itself has `ctx` and can log. API surface with
no user is not extensibility.

**Generics.** Rejected: resources are introspected at runtime; there is no
compile-time row type to parameterize over.

**The names `Chain` and `Request`.** Rejected: `chain()` already exists in this
package (`handler.go:58`) and means sequential middleware wrapping — a different
algorithm. `Request` collides with the `*http.Request` in scope at every call
site. `DenyOverrides` names its combining algorithm exactly, and is the term of
art.

**Postgres RLS as the primary mechanism.** Rejected; see the appendix.

**Host-defined capabilities.** Deferred. `Capability` is a string type, but no
exported entry point lets a host route a check through the pipeline. Adding one
is additive.

**Column-level authorization** (mask `salary` from support staff). An explicit
non-goal for v1, and a real gap: `Authorize` is per-resource, and a scope is
per-row, so neither expresses it. `Attributes` can gain a `Field` later without
breaking anyone; that is precisely what the struct is for.

## 8. Defects this design closes

| Defect | Where | Fix |
|--------|-------|-----|
| UI affordances derive from `res.writable()`, not the authorizer; denied operators see buttons that 403 | `handler_crud.go:154,210` | View model calls the same `allowed()` as the guard |
| A second authz vocabulary, `action.allowed func(Principal) bool`, no ctx, no error, AND-ed as a *second enforcement gate* | `action.go:27`, `handler_action.go:43` | Deleted; expressed as an authorizer in a `DenyOverrides` set |
| Per-resource authorizer **replaces** the admin-wide one, so it can *widen* access — and silently drops its row `Scope` | `handler.go:177-182` | `Resource.Authorize` deleted; authorization is admin-wide |
| FK label resolution reads unregistered, unauthorized tables, and resolves the referenced table by **bare name** across schemas | `handler_list.go:235,270` | `resolveRef` uses `fk.RefSchema`; `refResource` requires the same schema-qualified table. Scope-aware labels still pending. |
| `DELETE` has no version token; any check-then-delete is racy | `handler_mutate.go:219` | The predicate is in the `DELETE`'s own `WHERE` |
| Adding a capability breaks every host's build | `auth.go:41-49` | One method; unknown capabilities abstain, which denies |
| Bulk actions receive whatever keys were posted | `handler_action.go` | Keys pre-filtered through the scope |
| No row-level authorization at all | `auth.go:44-48` | `Scoper` |

---

## Appendix C — Implementation status

A second review pass (six independent reviewers, library-user lens) tightened the
surface and closed real defects. Changes from that pass:

- **Simplicity.** `Op` and its constants are gone; a `Constraint` is opaque and
  built only by `Eq`, `Ne`, or `In`, so it cannot be malformed (`checkArity` and
  `ErrBadScope` deleted with it). A pure row-restricting rule is a plain function
  wrapped in `ScopeOnly` — no no-op `Authorize` boilerplate. `AllowAll` is now a
  value, not a type.
- **Security — INSERT/UPDATE could write outside a scope.** An `Eq` constraint is
  now pinned on the write (`enforceScope`): the scoped column is written from the
  constraint value, overriding operator input and adding the column if absent, so
  a scoped operator can neither create nor move a row out of their scope. Proven
  against a live database.
- **Functional — FK labels broke under PgBouncer.** `BuildFKLabels` bound a `[]any`
  as one array argument, which pgx cannot encode in transaction-pooling modes.
  It now uses individual `IN ($1, $2, …)` placeholders, which encode in every mode.
- **`labelColumn`** now skips columns the referenced resource marks `hidden`, so a
  hidden column cannot resurface as an FK label.
- **`CountRowsInScope`** uses a single-column `count(DISTINCT k) … IN (…)` fast path
  (protocol-safe and duplicate/non-unique-key safe), falling back to the OR-of-ANDs
  form only for composite keys.
- Hygiene: `resolveFKLabels` checks `rows.Err()`; `vetActionKeys` scans a `bigint`
  into `int64`; the `cap` parameter no longer shadows the builtin.
- **Ergonomics.** `ActionFunc` now receives a `Keys` value instead of `[][]any`.
  Because the key columns are introspected, `Keys.Int64s` / `Keys.Strings` hand
  back a concretely-typed slice ready to bind to `= ANY($n)` — friendlier than a
  raw `[][]any`, and safer, since a `[]any` bound as an argument fails to encode
  under pgx's PgBouncer-compatible modes. `Raw` covers composite keys.
- Tests added for the fail-closed paths: `ScopeOnly`, `enforceScope` (overwrite /
  add-missing / ignore-non-equality), and `denyOverrides.Scope` error propagation.

Landed in the first pass:

- The reshape itself: `Decision` (zero value `Deny`), `Attributes`, one-method
  `Authorizer`, `AuthorizerFunc`, `DenyOverrides`, `Scoper`, `Constraint`.
- Scope pushdown into `BuildList` (list + export), `SelectRow` (detail + edit
  form), `UpdateRow`, `DeleteRow`, and `BuildFKLabels`.
- `ExistsRow` disambiguates a zero-row mutation: 409 if the row moved under us,
  404 if it is out of scope. Runs on the failure path only, in the same tx.
- `CountRowsInScope` vets a bulk action's keys inside the action's transaction.
  A key that is out of scope, absent, or duplicated refuses the whole action.
- The view model calls the same `can` the guard calls, so `CanCreate`, `CanEdit`,
  `CanDelete`, `HasDetail`, and `visibleActions` cannot drift from enforcement.
- `WithActionAllowed` and `action.allowed` deleted: action permissions are an
  `Authorizer` switching on `attrs.Action`.
- `query.OpNe` (`<>`) added, with the NULL caveat documented on `Ne`.

Behaviour changes worth knowing:

- A zero-row `DELETE` now reports 404 instead of a silent success. "Already gone"
  and "not yours" are deliberately indistinguishable.
- FK labels are resolved only for referenced tables that are registered resources
  the principal may view, and only within that resource's scope. Previously any
  referenced table was read.
- `ne` is now an accepted request-filter operator wherever `eq` is.

Also landed earlier (independent of the reshape):

- `Resource.Authorize` and `authorizerFor` deleted; `guard` reads `cfg.authorizer`.
- `resolveTable` rejects a name matching a table in more than one configured
  schema (`ErrAmbiguousTable`) instead of silently returning the first match.
- `autoRegister` snapshots the explicit-registration names before it runs. It
  previously tested the live resource map that its own `add` writes into, so its
  first `users` masked a second `users` from another schema as though a host had
  registered it by hand. Same-named tables now fail the build.
- `autoRegister` builds each resource from the table it discovered rather than
  re-resolving the bare name.
- Resource names must be URL-safe (`ErrUnsafeName`); auto-registration skips a
  table whose name cannot be a route, as it already skips unkeyed tables.
- FK label lookup resolves the referenced table through `fk.RefSchema`, and links
  only to a registered resource backed by that same schema-qualified table.

Remaining limits: an `Eq` scope is pinned on write, but `Ne` and `In` scopes
constrain only which rows a write may *target*, not the values written — mark such
columns `Readonly` if the operator must not set them. Column-level authorization
(masking a column) and a boolean `OR` across constraints remain out of scope for
v1; both are additive when a use case arrives.

---

## Appendix A — Postgres RLS

RLS is the right *complement* to this design and the wrong *primary* mechanism
for it. It is defense in depth: it still applies if a future route forgets its
guard, and it reaches inside host-authored action SQL that Go cannot rewrite.
Recommend it; do not depend on it.

It was rejected as primary for reasons that are specific and checkable:

1. **pgdesk's reads are not transactional.** `withTx` is called from four places,
   all mutations. List, detail, export, and FK-label lookups go through
   `runQuery` → `a.db.Query` directly on the pool, and a single list page touches
   several pooled connections. `set_config(..., true)` has `SET LOCAL` semantics
   and reverts at the end of each implicit single-statement transaction, so the
   GUC would be unset for exactly the queries RLS was introduced to filter.
   Making it work means wrapping the entire read layer in transactions, and
   streaming a CSV export inside an open transaction pins a pooled connection for
   the duration of the client's download.
2. **The obvious fix is a cross-request leak.** A host who discovers the GUC does
   not stick reaches for `set_config(..., false)`. That is session-scoped;
   `pgxpool` does not reset sessions on release, so the next request on that
   connection inherits the previous principal's subject.
3. **A denied write is indistinguishable from a lost update.** An RLS `WITH CHECK`
   failure yields zero rows affected, which pgdesk already reads as `errConflict`
   (`handler_crud.go:380`) and renders as *"someone else changed this row"*.
4. **RLS does not apply to the table owner** unless `FORCE ROW LEVEL SECURITY` is
   set, and never applies to a superuser or a `BYPASSRLS` role. An admin
   framework plausibly connects as the owner. The policies would exist, look
   correct, and enforce nothing.
5. **`current_setting` has a fail-open trap.** The one-argument form raises
   `unrecognized configuration parameter` for an unset custom GUC — loud, and
   fail-closed. The two-argument `missing_ok` form returns `NULL`, which invites
   the natural-looking policy `WHERE current_setting('pgdesk.subject', true) IS NULL OR owner = ...`;
   with the GUC never set on reads, that returns every row of every table.
6. **`ILIKE` is not leakproof.** Under RLS the planner will not apply a
   non-leakproof operator to rows the policy has not yet filtered, so a `pg_trgm`
   index on a searched column may stop being used. pgdesk's search box and its
   `ilike` filter would silently degrade to a sequential scan.

If a host wants RLS anyway, document this contract:

- Connect as a dedicated role that is **not** the table owner, **not** a
  superuser, and **without** `BYPASSRLS`.
- `ALTER TABLE ... ENABLE ROW LEVEL SECURITY` **and** `FORCE ROW LEVEL SECURITY`.
- Write policies with the one-argument `current_setting`, so a missing GUC errors
  rather than matching:
  ```sql
  CREATE POLICY posts_subject ON posts
    USING (author_id = current_setting('pgdesk.subject')::uuid);
  ```
  Never `current_setting('pgdesk.subject', true) IS NULL OR ...`.
- Do not `ALTER DATABASE ... SET pgdesk.subject = ''`; a declared default converts
  the loud failure into a silent one.
- No custom-GUC declaration is otherwise needed: `custom_variable_classes` was
  removed in PostgreSQL 9.2, and any dotted name is an accepted placeholder.

`WithSessionClaims` is **not** part of this design. If it is ever added, it must
set GUCs only via `select set_config($1, $2, true)` with bound parameters —
`SET LOCAL x = $1` does not accept a bind, and concatenating a principal ID into
a `SET` is an injection — and it must whitelist the GUC name to a namespaced
prefix, or a host will set `role` or `search_path` from a JWT claim.

## Appendix B — Migration

- `AllowAll` is now a value (`pgdesk.AllowAll`), not a type: `WithAuthorizer(pgdesk.AllowAll)`.
- Hosts implementing `Authorizer` collapse their seven methods into one `switch`,
  ending in `default: return Abstain, nil`.
- `Resource.Authorize(az)` is **removed**. Express the same rule as an authorizer
  in the admin-wide `DenyOverrides` set, switching on `attrs.Resource` — exactly
  as `frozenLedger` does in §5.2.
- `WithActionAllowed` is removed. The predicate moves into an `Authorizer` that
  denies `CapRunAction` for the action in question; `DenyOverrides` preserves the
  AND semantics it had.
- A nil admin-wide authorizer still denies everything: `CapAccessAdmin` is checked
  against `cfg.authorizer` alone, so an unset `WithAuthorizer` locks the admin
  regardless of any other configuration.
