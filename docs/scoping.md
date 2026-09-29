# How scoping is enforced

An `Authorizer` decides whether an operator may do something. If it also
implements `Scoper`, it decides which rows they may do it to:

```go
tenantOnly := pgdesk.ScopeOnly(func(ctx context.Context, attrs pgdesk.Attributes) ([]pgdesk.Constraint, error) {
	return []pgdesk.Constraint{pgdesk.Eq("tenant_id", tenantFrom(ctx))}, nil
})

pgdesk.WithAuthorizer(pgdesk.DenyOverrides(pgdesk.Roles{...}, tenantOnly))
```

The Scoper runs on every request that reads or writes rows. This page follows
one constraint from the Scoper to the SQL. If any step fails, the request is
refused. pgdesk never falls back to running the query without the scope.

## 1. The constraint

A `Constraint` can only be built with `Eq`, `Ne` or `In`. Its fields are
unexported, so a Scoper cannot hand pgdesk a SQL fragment, a custom operator or
a column expression. It returns a column name, one of three operators and plain
values. ([scope.go](../scope.go))

The Scoper is asked separately for each capability. `attrs.Capability` tells it
what is happening:

| Request | Scoped as |
|---|---|
| List page, CSV export, `options.json` | `CapList` |
| Detail page | `CapView` |
| Edit form and save | `CapUpdate` |
| Create | `CapCreate` |
| Delete | `CapDelete` |
| Bulk action | `CapRunAction`, with `attrs.Action` set |
| Foreign-key labels, search and pickers | `CapView` on the *referenced* resource |

Export is deliberately scoped as `CapList`, not `CapExport`. A Scoper written
before `CapExport` existed still limits what can be exported.

## 2. The column must exist

The column name is looked up in the catalog pgdesk read from `pg_catalog`, on
the resource's table. A name that isn't a real column of that table
(a typo, a column on a different table, a dropped column) is an error.
([`resolveConstraints`](../scope.go))

The error is logged as `pgdesk: scope resolution failed` and the operator gets a
403. A broken Scoper locks people out; it never widens what they see.

## 3. The operator must suit the column type

The operator is checked against the same allowlist that guards the filters in
the URL. It is keyed by the column's type category:
([internal/query/operator.go](../internal/query/operator.go))

| Column type | Allowed |
|---|---|
| text | `eq`, `ne`, `ilike`, `in`, `isnull` |
| numeric | `eq`, `ne`, `lt`, `gt`, `between`, `in`, `isnull` |
| timestamp | `eq`, `ne`, `lt`, `gt`, `between`, `isnull` |
| enum, uuid | `eq`, `ne`, `in`, `isnull` |
| bool | `eq`, `ne`, `isnull` |
| json | `isnull` |
| anything else | `eq`, `ne`, `isnull` |

So `In("payload", ...)` on a `jsonb` column fails here with a 403, instead of
reaching PostgreSQL and failing, or matching something unexpected.

## 4. The WHERE clause

The resolved constraints are ANDed onto the query, after any filters and search
the operator chose. The column name comes from the catalog and is quoted. Every
value is a `$n` parameter, never text in the SQL.
([`emitFilter`](../internal/query/list.go))

```sql
SELECT ... FROM "public"."orders"
WHERE "status" = $1          -- the operator's filter
  AND "tenant_id" = $2       -- the scope
```

Because operator filters and scope are ANDed together, no filter, search term
or URL parameter can see past the scope. The same applies to writes:

- **Update and delete** put the scope in the `WHERE` of the `UPDATE` or
  `DELETE`. A row outside the scope matches nothing and gets a 404, the same as
  a row that doesn't exist.
- **Bulk actions** count the selected rows that fall inside the scope, in the
  action's transaction. If any selected row is outside it, the whole action is
  refused. ([`vetActionKeys`](../handler_action.go))
- **Foreign keys** are labelled, searched and offered in pickers only through
  the referenced resource's `CapView` scope. Searching orders for "ada" does not
  find orders of an Ada from another tenant.

## 5. Writes stay in scope

Filtering which rows can be changed isn't enough. The new values could still
point somewhere the operator can't see. Two checks close that:

- **`Eq` constraints are pinned.** On create and update, a column with an `Eq`
  constraint is set to the scoped value, whatever the form sent. An operator
  can't create a row in another tenant or move one there.
  ([`enforceScope`](../handler_crud.go))
- **Foreign-key targets are checked.** For each foreign-key column being
  written, pgdesk looks the target up through the referenced resource's
  `CapView` scope, in the same transaction as the write. If the target is
  outside that scope, nothing is written and the field says "is not an
  available selection", even if the operator typed the ID by hand.
  ([`checkFKScope`](../fk.go))

## Limits

- Only `Eq` constraints are pinned on write. `In` and `Ne` limit which rows are
  read, updated and deleted, but not the values a create or update writes. If
  you scope with `In`, back it with a CHECK constraint, a trigger or row-level
  security.
- The foreign-key target check runs only when the operator has `CapView` on the
  referenced resource. Without it, the database's own foreign key is the only
  check on the value.
- Scoping protects pgdesk's own queries. Your custom bulk actions receive the
  selected keys after they have been checked, but anything else they query is
  up to them.
