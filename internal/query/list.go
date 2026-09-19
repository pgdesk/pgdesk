package query

import (
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

type Filter struct {
	Col    *introspect.Column
	Op     Operator
	Values []any
}

type SearchSpec struct {
	Cols []*introspect.Column
	Term string
}

type ListParams struct {
	Columns  []*introspect.Column
	Filters  []Filter
	Search   *SearchSpec
	Sort     *introspect.Column
	SortDesc bool
	Limit    int
	Offset   int
}

func BuildList(t *introspect.Table, p ListParams) (string, []any, error) {
	if len(p.Columns) == 0 {
		return "", nil, ErrNoColumns
	}
	var b strings.Builder
	args := &Args{}
	b.WriteString("SELECT ")
	writeColumnList(&b, p.Columns)
	b.WriteString(" FROM ")
	b.WriteString(QualifyIdent(t.Schema, t.Name))

	conds := make([]string, 0, len(p.Filters)+1)
	for _, f := range p.Filters {
		conds = append(conds, emitFilter(args, f))
	}
	if p.Search != nil && p.Search.Term != "" && len(p.Search.Cols) > 0 {
		conds = append(conds, emitSearch(args, p.Search))
	}
	if len(conds) > 0 {
		b.WriteString(" WHERE ")
		b.WriteString(strings.Join(conds, " AND "))
	}

	order := make([]string, 0, 1+len(t.PrimaryKey))
	if p.Sort != nil {
		dir := " ASC"
		if p.SortDesc {
			dir = " DESC"
		}
		order = append(order, Ident(p.Sort.Name)+dir)
	}
	for _, pk := range t.PrimaryKey {
		if p.Sort != nil && pk.Name == p.Sort.Name {
			continue
		}
		order = append(order, Ident(pk.Name)+" ASC")
	}
	if len(order) > 0 {
		b.WriteString(" ORDER BY ")
		b.WriteString(strings.Join(order, ", "))
	}
	b.WriteString(" LIMIT ")
	b.WriteString(args.Add(p.Limit))
	b.WriteString(" OFFSET ")
	b.WriteString(args.Add(p.Offset))
	return b.String(), args.Values(), nil
}

func emitFilter(args *Args, f Filter) string {
	col := Ident(f.Col.Name)
	switch f.Op {
	case OpEq:
		return col + " = " + args.Add(f.Values[0])
	case OpNe:
		return col + " <> " + args.Add(f.Values[0])
	case OpILike:
		return col + " ILIKE " + args.Add(f.Values[0])
	case OpLt:
		return col + " < " + args.Add(f.Values[0])
	case OpGt:
		return col + " > " + args.Add(f.Values[0])
	case OpBetween:
		return col + " BETWEEN " + args.Add(f.Values[0]) + " AND " + args.Add(f.Values[1])
	case OpIn:
		parts := make([]string, len(f.Values))
		for i, v := range f.Values {
			parts[i] = args.Add(v)
		}
		return col + " IN (" + strings.Join(parts, ", ") + ")"
	case OpIsNull:
		if isNull, _ := f.Values[0].(bool); isNull {
			return col + " IS NULL"
		}
		return col + " IS NOT NULL"
	default:

		return "false"
	}
}

func emitSearch(args *Args, s *SearchSpec) string {
	ph := args.Add(s.Term)
	parts := make([]string, len(s.Cols))
	for i, c := range s.Cols {
		parts[i] = Ident(c.Name) + " ILIKE " + ph
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

func BuildFKLabels(ref *introspect.Table, pkCol, labelCol *introspect.Column, pkVals []any, scope []Filter) (string, []any) {
	var b strings.Builder
	args := &Args{}
	b.WriteString("SELECT ")
	b.WriteString(Ident(pkCol.Name))
	b.WriteString(", ")
	b.WriteString(Ident(labelCol.Name))
	b.WriteString(" FROM ")
	b.WriteString(QualifyIdent(ref.Schema, ref.Name))
	b.WriteString(" WHERE ")
	b.WriteString(Ident(pkCol.Name))
	b.WriteString(" IN (")
	for i, v := range pkVals {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(args.Add(v))
	}
	b.WriteString(")")
	writeScope(&b, args, scope)
	return b.String(), args.Values()
}
