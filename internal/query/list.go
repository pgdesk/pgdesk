package query

import (
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

// Filter is a resolved, validated filter predicate. Col came from the catalog
// (D3), Op passed the type-category whitelist, and Values are typed via
// ParseScalar. Nothing here is request-string-interpolated: columns are quoted
// catalog identifiers and values become $N.
type Filter struct {
	Col    *introspect.Column
	Op     Operator
	Values []any // 1 for eq/lt/gt/ilike/isnull, 2 for between, N for in
}

// SearchSpec is a resolved free-text search across text columns.
type SearchSpec struct {
	Cols []*introspect.Column // text columns to match
	Term string               // already wrapped for ILIKE by the caller
}

// ListParams fully describes a list query. Every identifier is a resolved
// catalog column; every value flows through Args.
type ListParams struct {
	Columns  []*introspect.Column
	Filters  []Filter
	Search   *SearchSpec
	Sort     *introspect.Column
	SortDesc bool
	Limit    int
	Offset   int
}

// BuildList assembles a parameterized list query from resolved params (D3).
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

	if p.Sort != nil {
		b.WriteString(" ORDER BY ")
		b.WriteString(Ident(p.Sort.Name))
		if p.SortDesc {
			b.WriteString(" DESC")
		} else {
			b.WriteString(" ASC")
		}
	}
	b.WriteString(" LIMIT ")
	b.WriteString(args.Add(p.Limit))
	b.WriteString(" OFFSET ")
	b.WriteString(args.Add(p.Offset))
	return b.String(), args.Values(), nil
}

// emitFilter renders one predicate, binding values as $N. The column identifier
// is a quoted catalog name; the operator was whitelisted for its type category.
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
		// Unreachable: operators are validated before a Filter is constructed.
		// Emit a predicate that is always false rather than risk a bad clause.
		return "false"
	}
}

// emitSearch renders "(c1 ILIKE $x OR c2 ILIKE $x ...)" sharing one placeholder.
func emitSearch(args *Args, s *SearchSpec) string {
	ph := args.Add(s.Term)
	parts := make([]string, len(s.Cols))
	for i, c := range s.Cols {
		parts[i] = Ident(c.Name) + " ILIKE " + ph
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// BuildFKLabels builds the batched foreign-key label lookup (D4): one
// "SELECT pk, label FROM ref WHERE pk IN ($1, $2, ...)" for a whole page of FK
// values, instead of a generated JOIN or N+1 queries. pkVals are the distinct
// referenced key values collected across the page.
//
// The keys are bound as individual $N placeholders rather than a single array
// parameter. A []any array argument cannot be encoded when pgx runs without a
// prepared-statement describe step (QueryExecModeExec / SimpleProtocol, the modes
// used behind a transaction-pooling PgBouncer), so an array bind would silently
// break FK labels on that common topology. Individual placeholders encode in
// every mode. This mirrors emitFilter's handling of OpIn.
//
// scope holds the principal's row constraints on the REFERENCED resource (O6): a
// label is a read of another table, so it must obey that table's own scope.
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
