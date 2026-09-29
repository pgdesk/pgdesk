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

var errBadRequest = errors.New("pgdesk: bad list request")

type listRequest struct {
	filters  []query.Filter
	search   *query.SearchSpec
	sortCol  *introspect.Column
	sortDesc bool
	sortKey  string
	page     int
	pageSize int
	q        string
	rawFltr  map[string]string
}

func (a *Admin) parseListRequest(r *http.Request, res *Resource) (*listRequest, error) {
	q := r.URL.Query()
	lr := &listRequest{
		page:    parsePage(q.Get("page")),
		rawFltr: map[string]string{},
	}
	lr.pageSize = a.clampPageSize(parsePageSize(q.Get("page_size"), res.pageSize))

	if term := strings.TrimSpace(q.Get("q")); term != "" {
		if len(res.searchFields) > 0 {
			lr.q = term
			lr.search = &query.SearchSpec{Cols: textColumns(res.searchFields), Term: "%" + escapeLike(term) + "%"}
		}
	}

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
			continue
		}
		colName, opToken := splitFilterKey(key[len("f_"):])
		col, ok := allowed[colName]
		if !ok {
			return nil, errBadRequest
		}
		f, err := buildFilter(col, opToken, raw)
		if err != nil {
			return nil, errBadRequest
		}
		lr.filters = append(lr.filters, f)
		lr.rawFltr[key] = raw
	}

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
		return query.Filter{Col: col, Op: op, Values: []any{"%" + escapeLike(raw) + "%"}}, nil
	default:
		v, err := query.ParseScalar(col, raw)
		if err != nil {
			return query.Filter{}, err
		}
		return query.Filter{Col: col, Op: op, Values: []any{v}}, nil
	}
}

var likeEscaper = strings.NewReplacer(
	`\`, `\\`,
	`%`, `\%`,
	`_`, `\_`,
)

func escapeLike(s string) string {
	return likeEscaper.Replace(s)
}

func splitFilterKey(rest string) (col, op string) {
	if i := strings.LastIndex(rest, "__"); i >= 0 {
		return rest[:i], rest[i+2:]
	}
	return rest, "eq"
}

func parsePageSize(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return def
	}
	return n
}

func columnSet(cols []*introspect.Column) map[string]*introspect.Column {
	m := make(map[string]*introspect.Column, len(cols))
	for _, c := range cols {
		m[c.Name] = c
	}
	return m
}

func textColumns(cols []*introspect.Column) []*introspect.Column {
	var out []*introspect.Column
	for _, c := range cols {
		if c.Category == introspect.CatText {
			out = append(out, c)
		}
	}
	return out
}
