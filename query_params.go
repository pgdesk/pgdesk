package pgdesk

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

// errBadRequest signals a malformed or disallowed list parameter. It maps to a
// 400 and never leaks which internal check failed (F5). Every list parameter is
// validated against the catalog and the resource's declared filter/sort sets
// before any query runs (D3, fail-closed).
var errBadRequest = errors.New("pgdesk: bad list request")

// listRequest is the parsed, validated shape of a list-view request.
type listRequest struct {
	filters  []query.Filter
	search   *query.SearchSpec
	sortCol  *introspect.Column
	sortDesc bool
	sortKey  string // raw sort spec echoed into header links ("" = resource default)
	page     int
	pageSize int
	q        string            // raw search term (for the search box)
	rawFltr  map[string]string // filter param key → raw value, for form repopulation
}

// parseListRequest resolves and validates all list parameters. Filter columns
// must be in the resource's declared Filters set; sort columns must be shown in
// the list; operators must pass the type-category whitelist; values must parse
// to their column type. Any violation returns errBadRequest (400).
func (a *Admin) parseListRequest(r *http.Request, res *Resource) (*listRequest, error) {
	q := r.URL.Query()
	lr := &listRequest{
		page:    parsePage(q.Get("page")),
		rawFltr: map[string]string{},
	}
	lr.pageSize = a.clampPageSize(parsePageSize(q.Get("page_size"), res.pageSize))

	// Search across the resource's text search fields.
	if term := strings.TrimSpace(q.Get("q")); term != "" {
		cols := textColumns(res.searchFields)
		if len(cols) > 0 {
			lr.q = term
			lr.search = &query.SearchSpec{Cols: cols, Term: "%" + term + "%"}
		}
	}

	// Filters: iterate f_* params in a stable order for deterministic SQL.
	allowed := columnSet(res.filters)
	var keys []string
	for k := range q {
		if strings.HasPrefix(k, "f_") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw := strings.TrimSpace(q.Get(key))
		if raw == "" {
			continue // an empty filter input is simply not applied
		}
		colName, opToken := splitFilterKey(key[len("f_"):])
		col, ok := allowed[colName]
		if !ok {
			return nil, errBadRequest // unknown or non-filterable column
		}
		f, err := buildFilter(col, opToken, raw)
		if err != nil {
			return nil, errBadRequest
		}
		lr.filters = append(lr.filters, f)
		lr.rawFltr[key] = raw
	}

	// Sort: only columns shown in the list are sortable.
	if spec := q.Get("sort"); spec != "" {
		name, desc := spec, false
		if strings.HasPrefix(spec, "-") {
			name, desc = spec[1:], true
		}
		col, ok := columnSet(res.listDisplay)[name]
		if !ok {
			return nil, errBadRequest
		}
		lr.sortCol, lr.sortDesc, lr.sortKey = col, desc, spec
	} else {
		lr.sortCol, lr.sortDesc = res.sortCol, res.sortDesc
	}

	return lr, nil
}

// buildFilter validates the operator against the column's type category and
// parses the value(s) to the column type (D3). All values become $N.
func buildFilter(col *introspect.Column, opToken, raw string) (query.Filter, error) {
	op, err := query.ParseOperator(col, opToken)
	if err != nil {
		return query.Filter{}, err
	}
	switch op {
	case query.OpIn:
		var vals []any
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			v, err := query.ParseScalar(col, part)
			if err != nil {
				return query.Filter{}, err
			}
			vals = append(vals, v)
		}
		if len(vals) == 0 {
			return query.Filter{}, errBadRequest
		}
		return query.Filter{Col: col, Op: op, Values: vals}, nil
	case query.OpBetween:
		parts := strings.SplitN(raw, ",", 2)
		if len(parts) != 2 {
			return query.Filter{}, errBadRequest
		}
		lo, err := query.ParseScalar(col, strings.TrimSpace(parts[0]))
		if err != nil {
			return query.Filter{}, err
		}
		hi, err := query.ParseScalar(col, strings.TrimSpace(parts[1]))
		if err != nil {
			return query.Filter{}, err
		}
		return query.Filter{Col: col, Op: op, Values: []any{lo, hi}}, nil
	case query.OpIsNull:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return query.Filter{}, err
		}
		return query.Filter{Col: col, Op: op, Values: []any{b}}, nil
	case query.OpILike:
		return query.Filter{Col: col, Op: op, Values: []any{"%" + raw + "%"}}, nil
	default: // eq, lt, gt
		v, err := query.ParseScalar(col, raw)
		if err != nil {
			return query.Filter{}, err
		}
		return query.Filter{Col: col, Op: op, Values: []any{v}}, nil
	}
}

// splitFilterKey splits "col__op" into (col, op), defaulting op to "eq". It uses
// the last "__" so single-underscore column names (created_at) are preserved.
func splitFilterKey(rest string) (col, op string) {
	if i := strings.LastIndex(rest, "__"); i >= 0 {
		return rest[:i], rest[i+2:]
	}
	return rest, "eq"
}

// parsePageSize parses ?page_size, falling back to the resource default. The hard
// maximum is applied separately by clampPageSize (F6).
func parsePageSize(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return def
	}
	return n
}

// columnSet indexes columns by name for O(1) membership checks.
func columnSet(cols []*introspect.Column) map[string]*introspect.Column {
	m := make(map[string]*introspect.Column, len(cols))
	for _, c := range cols {
		m[c.Name] = c
	}
	return m
}

// textColumns keeps only text-category columns, so ILIKE search never targets a
// column type it cannot match.
func textColumns(cols []*introspect.Column) []*introspect.Column {
	var out []*introspect.Column
	for _, c := range cols {
		if c.Category == introspect.CatText {
			out = append(out, c)
		}
	}
	return out
}
