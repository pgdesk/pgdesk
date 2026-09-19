package pgdesk

import (
	"net/http"

	"github.com/pgdesk/pgdesk/internal/introspect"
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
