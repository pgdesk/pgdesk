package pgdesk

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
	"github.com/pgdesk/pgdesk/internal/render"
)

// listBase returns the absolute list path for a resource (base path is stripped
// from r.URL.Path inside the handler, so it is re-prepended here).
func (a *Admin) listBase(res *Resource) string {
	return a.cfg.basePath + "/" + res.name
}

// withQuery builds listBase + "?" + encoded, preserving current parameters with
// the given overrides applied (empty override value deletes the key).
func (a *Admin) listURL(r *http.Request, res *Resource, overrides map[string]string) string {
	q := cloneQuery(r.URL.Query())
	for k, v := range overrides {
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return a.listBase(res)
	}
	return a.listBase(res) + "?" + q.Encode()
}

// pageURL returns the current list URL with the page set to n (page 1 omits the
// param for a clean canonical URL).
func (a *Admin) pageURL(r *http.Request, res *Resource, n int) string {
	page := ""
	if n > 1 {
		page = strconv.Itoa(n)
	}
	return a.listURL(r, res, map[string]string{"page": page})
}

// sortHeaders builds a clickable header per displayed column. Clicking sorts
// ascending; clicking the active column toggles to descending. Sorting resets to
// page 1 and preserves filters/search.
func (a *Admin) sortHeaders(r *http.Request, res *Resource, display []*introspect.Column, lr *listRequest) []sortHeader {
	headers := make([]sortHeader, len(display))
	for i, c := range display {
		active := lr.sortCol != nil && lr.sortCol.Name == c.Name
		next := c.Name // ascending
		if active && !lr.sortDesc {
			next = "-" + c.Name // toggle to descending
		}
		headers[i] = sortHeader{
			Label:  a.fieldLabel(res, c),
			URL:    a.listURL(r, res, map[string]string{"sort": next, "page": ""}),
			Active: active,
			Desc:   active && lr.sortDesc,
		}
	}
	return headers
}

// filterFields builds the filter form controls from the resource's declared
// filters, pre-filled with the current values.
func (a *Admin) filterFields(res *Resource, lr *listRequest) []filterField {
	fields := make([]filterField, 0, len(res.filters))
	for _, c := range res.filters {
		label := a.fieldLabel(res, c)
		switch c.Category {
		case introspect.CatEnum:
			fields = append(fields, filterField{
				Label: label, Kind: "select", ParamKey: "f_" + c.Name,
				Value: lr.rawFltr["f_"+c.Name], Options: append([]string{""}, c.EnumLabels...),
			})
		case introspect.CatBool:
			fields = append(fields, filterField{
				Label: label, Kind: "bool", ParamKey: "f_" + c.Name,
				Value: lr.rawFltr["f_"+c.Name], Options: []string{"", "true", "false"},
			})
		case introspect.CatTimestamp:
			fields = append(fields, filterField{
				Label: label, Kind: "daterange",
				ParamKey: "f_" + c.Name + "__gt", ParamKeyTo: "f_" + c.Name + "__lt",
				Value: lr.rawFltr["f_"+c.Name+"__gt"], ValueTo: lr.rawFltr["f_"+c.Name+"__lt"],
			})
		case introspect.CatText:
			fields = append(fields, filterField{
				Label: label, Kind: "text", ParamKey: "f_" + c.Name + "__ilike",
				Value: lr.rawFltr["f_"+c.Name+"__ilike"],
			})
		default: // numeric, uuid -> exact match
			fields = append(fields, filterField{
				Label: label, Kind: "text", ParamKey: "f_" + c.Name,
				Value: lr.rawFltr["f_"+c.Name],
			})
		}
	}
	return fields
}

// Filter controls beyond listFilterInlineMax collapse into a "More filters"
// popover; the first listFilterPinned stay inline. Small filter sets stay wholly
// inline so a single-line bar never sprouts a needless disclosure.
const (
	listFilterInlineMax = 4
	listFilterPinned    = 3
)

// filterViews builds the list's filter controls split into the always-visible
// inline set and the overflow set (rendered in the "More filters" popover), plus
// removable chips for the filters that currently carry a value.
func (a *Admin) filterViews(r *http.Request, res *Resource, lr *listRequest) (inline, overflow []filterField, chips []filterChip) {
	all := a.filterFields(res, lr)
	inline, overflow = splitFilters(all)
	for _, f := range all {
		if chip, ok := a.filterChip(r, res, f); ok {
			chips = append(chips, chip)
		}
	}
	return inline, overflow, chips
}

// splitFilters partitions filter controls into the inline set and the overflow
// set. Small sets stay wholly inline; larger ones keep listFilterPinned inline
// and push the rest into the "More filters" popover.
func splitFilters(all []filterField) (inline, overflow []filterField) {
	if len(all) <= listFilterInlineMax {
		return all, nil
	}
	return all[:listFilterPinned], all[listFilterPinned:]
}

// filterChip returns a removable chip for an active filter field, or ok=false
// when the field carries no value. The chip's URL clears that filter's
// parameter(s) and resets pagination.
func (a *Admin) filterChip(r *http.Request, res *Resource, f filterField) (filterChip, bool) {
	overrides := map[string]string{"page": ""}
	var display string
	if f.Kind == "daterange" {
		if f.Value == "" && f.ValueTo == "" {
			return filterChip{}, false
		}
		overrides[f.ParamKey] = ""
		overrides[f.ParamKeyTo] = ""
		display = dateRangeLabel(f.Value, f.ValueTo)
	} else {
		if f.Value == "" {
			return filterChip{}, false
		}
		overrides[f.ParamKey] = ""
		display = f.Value
	}
	return filterChip{Label: f.Label, Value: display, RemoveURL: a.listURL(r, res, overrides)}, true
}

// dateRangeLabel renders a daterange filter's active value for a chip, handling
// open-ended ranges (only a lower or only an upper bound).
func dateRangeLabel(from, to string) string {
	switch {
	case from != "" && to != "":
		return from + " -> " + to
	case from != "":
		return ">= " + from
	default:
		return "<= " + to
	}
}

// visibleActions returns the actions the current principal may run. It consults
// the same authorizer the guard consults, so the action bar never offers a button
// that would 403 (O6).
func (a *Admin) visibleActions(r *http.Request, res *Resource) []actionMeta {
	if len(res.actionOrder) == 0 {
		return nil
	}
	var out []actionMeta
	for _, name := range res.actionOrder {
		act := res.actions[name]
		if !a.can(r, CapRunAction, res.name, act.name) {
			continue
		}
		out = append(out, actionMeta{Name: act.name, Label: act.label, Confirm: act.confirm})
	}
	return out
}

// exportURL builds the CSV export link, carrying the current filters/search/sort
// (but not pagination) so the export matches what the operator is viewing.
func (a *Admin) exportURL(r *http.Request, res *Resource) string {
	q := cloneQuery(r.URL.Query())
	q.Del("page")
	base := a.cfg.basePath + "/" + res.name + "/export.csv"
	if len(q) == 0 {
		return base
	}
	return base + "?" + q.Encode()
}

// fkLabel is a resolved foreign-key display label plus an optional detail link.
type fkLabel struct {
	label string
	link  string
}

// buildCell renders one list cell, substituting a resolved FK label/link when
// available (D4).
func (a *Admin) buildCell(res *Resource, col *introspect.Column, value any, labels map[string]map[any]fkLabel) cellView {
	if m, ok := labels[col.Name]; ok {
		if fl, ok := m[value]; ok {
			return cellView{Value: value, Label: fl.label, Link: fl.link}
		}
	}
	return cellView{Value: value}
}

// resolveFKLabels performs the batched FK label lookups for the page (D4): one
// "SELECT pk, label FROM ref WHERE pk = ANY($1)" per foreign-key column, mapping
// results back in Go. No generated JOINs, no N+1.
func (a *Admin) resolveFKLabels(r *http.Request, res *Resource, display []*introspect.Column, pageCells [][]any) map[string]map[any]fkLabel {
	st := stateFromContext(r.Context())
	if st == nil || len(pageCells) == 0 {
		return nil
	}
	out := map[string]map[any]fkLabel{}
	for ci, col := range display {
		fk := singleColumnFK(res.table, col.Name)
		if fk == nil {
			continue
		}
		ref, ok := resolveRef(st.catalog, fk)
		if !ok {
			continue
		}
		// A label is a read of another table, so it obeys that table's own rules:
		// only a registered resource the principal may view is ever read, and only
		// within that resource's row scope (O6).
		refRes, ok := refResource(st, ref)
		if !ok || !a.can(r, CapView, refRes.name, "") {
			continue
		}
		refScope, serr := a.scopeFor(r, refRes, CapView, "")
		if serr != nil {
			LoggerFromContext(r.Context()).Error("pgdesk: FK label scope failed",
				"resource", refRes.name, "error", serr)
			continue
		}
		refPK, ok := ref.Column(fk.RefColumns[0])
		if !ok {
			continue
		}
		labelCol := labelColumn(refRes, refPK)

		// Collect distinct non-nil FK values across the page.
		seen := map[any]bool{}
		var values []any
		for _, cells := range pageCells {
			v := cells[ci]
			if v == nil || seen[v] {
				continue
			}
			seen[v] = true
			values = append(values, v)
		}
		if len(values) == 0 {
			continue
		}

		sql, args := query.BuildFKLabels(ref, refPK, labelCol, values, refScope)
		ctx, cancel := a.queryContext(r)
		rows, qerr := a.runQuery(ctx, "fk_labels", sql, args)
		if qerr != nil {
			cancel()
			LoggerFromContext(r.Context()).Warn("pgdesk: FK label lookup failed",
				"column", col.Name, "ref", fk.RefTable, "error", qerr)
			continue
		}
		m := map[any]fkLabel{}
		for rows.Next() {
			vals, verr := rows.Values()
			if verr != nil || len(vals) < 2 {
				continue
			}
			pkVal, lbl := vals[0], vals[1]
			fl := fkLabel{label: render.FormatValue(lbl)}
			if seg, err := query.EncodeKey([]*introspect.Column{refPK}, []any{pkVal}); err == nil {
				fl.link = a.cfg.basePath + "/" + refRes.name + "/" + seg
			}
			m[pkVal] = fl
		}
		rowsErr := rows.Err()
		rows.Close()
		cancel()
		if rowsErr != nil {
			// A mid-stream failure leaves m partial; log it rather than silently
			// presenting an incomplete label set as if some FKs simply had no match.
			LoggerFromContext(r.Context()).Warn("pgdesk: FK label lookup incomplete",
				"column", col.Name, "ref", fk.RefTable, "error", rowsErr)
		}
		out[col.Name] = m
	}
	return out
}

// resolveRef resolves the table a foreign key references, using the schema the
// key itself records. Resolving by bare name across the configured schemas would
// let a reference to billing.accounts read public.accounts and surface the wrong
// table's data as labels. A reference outside the exposed schemas is not in the
// catalog and simply does not resolve.
func resolveRef(cat *introspect.Catalog, fk *introspect.ForeignKey) (*introspect.Table, bool) {
	return cat.Table(fk.RefSchema, fk.RefTable)
}

// refResource returns the registered resource for a referenced table, if there is
// one. Resources are keyed by bare name, so the resource found under a table's
// name may be backed by a same-named table in another schema; linking to it would
// send the operator to a different table than the one whose label was read.
func refResource(st *adminState, ref *introspect.Table) (*Resource, bool) {
	res, ok := st.resource(ref.Name)
	if !ok || res.table.Schema != ref.Schema || res.table.Name != ref.Name {
		return nil, false
	}
	return res, true
}

// singleColumnFK returns the single-column foreign key on t whose local column is
// colName, or nil. Composite FKs are not label-substituted in v1.
func singleColumnFK(t *introspect.Table, colName string) *introspect.ForeignKey {
	for _, fk := range t.ForeignKeys {
		if len(fk.Columns) == 1 && len(fk.RefColumns) == 1 && fk.Columns[0] == colName {
			return fk
		}
	}
	return nil
}

// labelColumn picks a human-friendly label column for a referenced table: the
// first text column that is not the referenced key, falling back to the key
// itself. A future release lets a resource declare this explicitly.
func labelColumn(ref *Resource, refPK *introspect.Column) *introspect.Column {
	for _, c := range ref.table.Columns() {
		if c.Name == refPK.Name || c.Category != introspect.CatText {
			continue
		}
		// A column the referenced resource hides must not resurface as an FK label:
		// hidden is how a host says "this text exists but must never be shown".
		if fc := ref.fields[c.Name]; fc != nil && fc.hidden {
			continue
		}
		return c
	}
	return refPK
}

// cloneQuery deep-copies url.Values so overrides don't mutate the request.
func cloneQuery(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		cp := make([]string, len(vs))
		copy(cp, vs)
		out[k] = cp
	}
	return out
}
