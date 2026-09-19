package pgdesk

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

// fkRef is a single-column foreign key resolved to the registered resource it
// points at, together with that resource's referenced key column and the column
// used to label its rows.
type fkRef struct {
	resource *Resource
	table    *introspect.Table
	pk       *introspect.Column
	label    *introspect.Column
}

// resolveFKRef resolves colName's single-column foreign key to a registered
// resource. It fails closed at every step: a composite foreign key, a reference
// outside the configured schemas, a referenced table the host never registered,
// or a principal without CapView on it all yield no reference -- and therefore no
// label and no picker. That is what keeps "nothing is exposed until you name it"
// true for data reached indirectly through a foreign key.
func (a *Admin) resolveFKRef(r *http.Request, res *Resource, colName string) (fkRef, bool) {
	st := stateFromContext(r.Context())
	if st == nil {
		return fkRef{}, false
	}
	fk := singleColumnFK(res.table, colName)
	if fk == nil {
		return fkRef{}, false
	}
	ref, ok := resolveRef(st.catalog, fk)
	if !ok {
		return fkRef{}, false
	}
	refRes, ok := refResource(st, ref)
	if !ok || !a.can(r, CapView, refRes.name, "") {
		return fkRef{}, false
	}
	refPK, ok := ref.Column(fk.RefColumns[0])
	if !ok {
		return fkRef{}, false
	}
	return fkRef{
		resource: refRes,
		table:    ref,
		pk:       refPK,
		label:    labelColumn(refRes, refPK),
	}, true
}

// resolveRowFKLabels resolves the foreign-key labels for a single row, keyed by
// column name. The detail page and the edit form both need "who is row 7?" for
// one row; this reuses the list's batched lookup so all three agree on the label,
// the authorization and the row scope applied to reach it.
func (a *Admin) resolveRowFKLabels(r *http.Request, res *Resource, cols []*introspect.Column, row map[string]any) map[string]fkLabel {
	if len(cols) == 0 || row == nil {
		return nil
	}
	cells := make([]any, len(cols))
	for i, c := range cols {
		cells[i] = row[c.Name]
	}
	byColumn := a.resolveFKLabels(r, res, cols, [][]any{cells})
	if len(byColumn) == 0 {
		return nil
	}
	out := make(map[string]fkLabel, len(byColumn))
	for i, c := range cols {
		if fl, ok := byColumn[c.Name][cells[i]]; ok {
			out[c.Name] = fl
		}
	}
	return out
}

// fkScopeError reports that a submitted foreign-key value points at a row outside
// the scope the principal has on the referenced resource.
type fkScopeError struct{ column string }

func (e *fkScopeError) Error() string {
	return "pgdesk: " + e.column + " references a row outside the principal's scope"
}

// checkFKScope verifies that every submitted foreign-key value points at a row the
// principal may reach on the referenced resource. It runs inside the mutation's
// own transaction, so no window exists between the check and the write.
//
// Without it the picker's option list is advisory: it offers only in-scope rows,
// but a hand-written POST could submit any key -- creating a reference across a
// scope boundary, and turning the accept/reject answer into an oracle for which
// keys exist in a table the operator cannot read.
//
// The check applies exactly where the picker applies -- resolveFKRef gates both --
// so a foreign key to an unregistered table, or one the principal may not view,
// keeps its existing plain-input behavior rather than gaining a new restriction.
// A referenced resource with no scope constrains nothing and costs no query.
func (a *Admin) checkFKScope(ctx context.Context, tx pgx.Tx, r *http.Request, res *Resource, cols []*introspect.Column, vals []any) error {
	for i, c := range cols {
		if i >= len(vals) || vals[i] == nil {
			continue // a NULL reference is not a reference
		}
		ref, ok := a.resolveFKRef(r, res, c.Name)
		if !ok {
			continue
		}
		// CapView is the visibility question -- "which rows of this resource exist
		// for me" -- and is the same capability resolveFKRef just authorized.
		refScope, err := a.scopeFor(r, ref.resource, CapView, "")
		if err != nil {
			return err
		}
		if len(refScope) == 0 {
			continue
		}
		// A fresh slice: appending to the caller's scope would mutate it.
		filters := make([]query.Filter, 0, len(refScope)+1)
		filters = append(filters, refScope...)
		filters = append(filters, query.Filter{Col: ref.pk, Op: query.OpEq, Values: []any{vals[i]}})

		sql, args, err := query.BuildList(ref.table, query.ListParams{
			Columns: []*introspect.Column{ref.pk},
			Filters: filters,
			Limit:   1,
		})
		if err != nil {
			return err
		}
		rows, qerr := tx.Query(ctx, sql, args...)
		if qerr != nil {
			return qerr
		}
		inScope := rows.Next()
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if !inScope {
			return &fkScopeError{column: c.Name}
		}
	}
	return nil
}
