package pgdesk

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
	"github.com/pgdesk/pgdesk/internal/render"
)

func (a *Admin) listBase(res *Resource) string {
	return a.cfg.basePath + "/" + res.name
}

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

func (a *Admin) pageURL(r *http.Request, res *Resource, n int) string {
	page := ""
	if n > 1 {
		page = strconv.Itoa(n)
	}
	return a.listURL(r, res, map[string]string{"page": page})
}

func (a *Admin) sortHeaders(r *http.Request, res *Resource, display []*introspect.Column, lr *listRequest) []sortHeader {
	headers := make([]sortHeader, len(display))
	for i, c := range display {
		active := lr.sortCol != nil && lr.sortCol.Name == c.Name
		next := c.Name
		if active && !lr.sortDesc {
			next = "-" + c.Name
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
			if len(c.Choices) > 0 {
				fields = append(fields, filterField{
					Label: label, Kind: "select", ParamKey: "f_" + c.Name,
					Value: lr.rawFltr["f_"+c.Name], Options: append([]string{""}, c.Choices...),
				})
				continue
			}
			fields = append(fields, filterField{
				Label: label, Kind: "text", ParamKey: "f_" + c.Name + "__ilike",
				Value: lr.rawFltr["f_"+c.Name+"__ilike"],
			})
		default:
			fields = append(fields, filterField{
				Label: label, Kind: "text", ParamKey: "f_" + c.Name,
				Value: lr.rawFltr["f_"+c.Name],
			})
		}
	}
	return fields
}

const (
	listFilterInlineMax = 4
	listFilterPinned    = 3
)

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

func splitFilters(all []filterField) (inline, overflow []filterField) {
	if len(all) <= listFilterInlineMax {
		return all, nil
	}
	return all[:listFilterPinned], all[listFilterPinned:]
}

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

func (a *Admin) exportURL(r *http.Request, res *Resource) string {
	if !a.can(r, CapExport, res.name, "") {
		return ""
	}
	q := cloneQuery(r.URL.Query())
	q.Del("page")
	base := a.cfg.basePath + "/" + res.name + "/export.csv"
	if len(q) == 0 {
		return base
	}
	return base + "?" + q.Encode()
}

type fkLabel struct {
	label string
	link  string
}

func (a *Admin) buildCell(res *Resource, col *introspect.Column, value any, labels map[string]map[any]fkLabel) cellView {
	if m, ok := labels[col.Name]; ok {
		if fl, ok := m[value]; ok {
			return cellView{Value: value, Label: fl.label, Link: fl.link}
		}
	}
	return cellView{Value: value}
}

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

			LoggerFromContext(r.Context()).Warn("pgdesk: FK label lookup incomplete",
				"column", col.Name, "ref", fk.RefTable, "error", rowsErr)
		}
		out[col.Name] = m
	}
	return out
}

func resolveRef(cat *introspect.Catalog, fk *introspect.ForeignKey) (*introspect.Table, bool) {
	return cat.Table(fk.RefSchema, fk.RefTable)
}

func refResource(st *adminState, ref *introspect.Table) (*Resource, bool) {
	res, ok := st.resource(ref.Name)
	if !ok || res.table.Schema != ref.Schema || res.table.Name != ref.Name {
		return nil, false
	}
	return res, true
}

func singleColumnFK(t *introspect.Table, colName string) *introspect.ForeignKey {
	for _, fk := range t.ForeignKeys {
		if len(fk.Columns) == 1 && len(fk.RefColumns) == 1 && fk.Columns[0] == colName {
			return fk
		}
	}
	return nil
}

func labelColumn(ref *Resource, refPK *introspect.Column) *introspect.Column {
	if ref.labelCol != nil {
		return ref.labelCol
	}
	for _, c := range ref.table.Columns() {
		if c.Name == refPK.Name || c.Category != introspect.CatText {
			continue
		}

		if fc := ref.fields[c.Name]; fc != nil && fc.hidden {
			continue
		}
		return c
	}
	return refPK
}

func cloneQuery(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		cp := make([]string, len(vs))
		copy(cp, vs)
		out[k] = cp
	}
	return out
}
