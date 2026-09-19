package pgdesk

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

type fkRef struct {
	resource *Resource
	table    *introspect.Table
	pk       *introspect.Column
	label    *introspect.Column
}

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

type fkScopeError struct{ column string }

func (e *fkScopeError) Error() string {
	return "pgdesk: " + e.column + " references a row outside the principal's scope"
}

func (a *Admin) checkFKScope(ctx context.Context, tx pgx.Tx, r *http.Request, res *Resource, cols []*introspect.Column, vals []any) error {
	for i, c := range cols {
		if i >= len(vals) || vals[i] == nil {
			continue
		}
		ref, ok := a.resolveFKRef(r, res, c.Name)
		if !ok {
			continue
		}

		refScope, err := a.scopeFor(r, ref.resource, CapView, "")
		if err != nil {
			return err
		}
		if len(refScope) == 0 {
			continue
		}

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
