package pgdesk

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

const (
	defaultRelatedLimit = 50
	maxRelatedLimit     = 500
)

type relatedSpec struct {
	child string
	via   string
	cols  []string
	sort  string
	limit int
	title string
}

// RelatedOption configures a section added with Resource.Related.
type RelatedOption func(*relatedSpec)

// RelatedVia names the child's referencing column; needed only when there are several.
func RelatedVia(column string) RelatedOption {
	return func(s *relatedSpec) { s.via = column }
}

// RelatedColumns sets the columns shown. Default: the child's list columns minus the referencing one.
func RelatedColumns(columns ...string) RelatedOption {
	return func(s *relatedSpec) { s.cols = columns }
}

// RelatedSort orders the rows ("-col" for descending). Default: the child's default sort.
func RelatedSort(spec string) RelatedOption {
	return func(s *relatedSpec) { s.sort = spec }
}

// RelatedLimit caps the rows shown (default 50, max 500).
func RelatedLimit(n int) RelatedOption {
	return func(s *relatedSpec) {
		if n > 0 {
			s.limit = min(n, maxRelatedLimit)
		}
	}
}

// RelatedTitle sets the heading. Default: the child's plural label.
func RelatedTitle(title string) RelatedOption {
	return func(s *relatedSpec) { s.title = title }
}

// Related lists the child's rows that reference the viewed row, read-only. The child must be registered; its hidden
// columns, list permission and Scoper apply. Needs a single-column key.
func (r *Resource) Related(child string, opts ...RelatedOption) {
	spec := &relatedSpec{child: child, limit: defaultRelatedLimit}
	for _, o := range opts {
		o(spec)
	}
	r.relateds = append(r.relateds, spec)
}

type relatedSection struct {
	child *Resource
	fk    *introspect.Column
	cols  []*introspect.Column
	sort  *introspect.Column
	desc  bool
	limit int
	title string
}

// resolveRelated validates every Related at startup.
func (a *Admin) resolveRelated(resources map[string]*Resource, order []string) error {
	for _, name := range order {
		res := resources[name]
		for _, spec := range res.relateds {
			sec, err := a.resolveOneRelated(res, spec, resources)
			if err != nil {
				return fmt.Errorf("resource %q: Related(%q): %w", name, spec.child, err)
			}
			res.sections = append(res.sections, sec)
		}
	}
	return nil
}

func (a *Admin) resolveOneRelated(parent *Resource, spec *relatedSpec, resources map[string]*Resource) (*relatedSection, error) {
	child, ok := resources[spec.child]
	if !ok {
		return nil, fmt.Errorf("no resource with that name; register it with WithResource")
	}
	if len(parent.keyCols) != 1 {
		return nil, fmt.Errorf("%q needs a single-column key to have related rows", parent.name)
	}
	fk, err := referenceTo(parent, child, spec.via)
	if err != nil {
		return nil, err
	}
	fkCol, _ := child.table.Column(fk.Columns[0])

	sec := &relatedSection{child: child, fk: fkCol, limit: spec.limit, title: spec.title}
	if sec.title == "" {
		sec.title = child.LabelPlural
	}
	if sec.cols, err = relatedColumns(child, spec.cols, fkCol); err != nil {
		return nil, err
	}
	sec.sort, sec.desc = child.sortCol, child.sortDesc
	if spec.sort != "" {
		name, desc := strings.CutPrefix(spec.sort, "-")
		col, ok := child.table.Column(name)
		if !ok {
			return nil, fmt.Errorf("RelatedSort: %q has no column %q", child.name, name)
		}
		sec.sort, sec.desc = col, desc
	}
	if !child.table.HasIndexOn(fkCol.Name) {
		a.cfg.logger.Warn("pgdesk: related rows will scan the table: no index begins with the referencing column",
			"resource", parent.name, "related", child.name, "column", fkCol.Name)
	}
	return sec, nil
}

func referenceTo(parent, child *Resource, via string) (*introspect.ForeignKey, error) {
	var found []*introspect.ForeignKey
	for _, fk := range child.table.ForeignKeys {
		if len(fk.Columns) == 1 && fk.RefSchema == parent.table.Schema && fk.RefTable == parent.table.Name && (via == "" || fk.Columns[0] == via) {
			found = append(found, fk)
		}
	}
	switch {
	case len(found) == 0 && via != "":
		return nil, fmt.Errorf("%q has no single-column foreign key %q to %q", child.name, via, parent.name)
	case len(found) == 0:
		return nil, fmt.Errorf("%q has no single-column foreign key to %q", child.name, parent.name)
	case len(found) > 1:
		cols := make([]string, len(found))
		for i, fk := range found {
			cols[i] = fk.Columns[0]
		}
		return nil, fmt.Errorf("%q has several foreign keys to %q (%s); choose one with RelatedVia", child.name, parent.name, strings.Join(cols, ", "))
	}
	if found[0].RefColumns[0] != parent.keyCols[0].Name {
		return nil, fmt.Errorf("the foreign key %q must reference the key of %q", found[0].Columns[0], parent.name)
	}
	return found[0], nil
}

func relatedColumns(child *Resource, names []string, fk *introspect.Column) ([]*introspect.Column, error) {
	if len(names) == 0 {
		cols := make([]*introspect.Column, 0, len(child.listDisplay))
		for _, c := range child.listDisplay {
			if c.Name != fk.Name {
				cols = append(cols, c)
			}
		}
		return cols, nil
	}
	cols := make([]*introspect.Column, 0, len(names))
	for _, n := range names {
		c, ok := child.table.Column(n)
		if !ok {
			return nil, fmt.Errorf("RelatedColumns: %q has no column %q", child.name, n)
		}
		cols = append(cols, c)
	}
	return cols, nil
}

type relatedView struct {
	Title     string
	Headers   []string
	Rows      []rowView
	Resource  string
	HasDetail bool
	Limit     int
	More      bool
	AllURL    string
}

// relatedViews skips a section that fails, so it cannot break the page.
func (a *Admin) relatedViews(r *http.Request, res *Resource, key any) []relatedView {
	var out []relatedView
	for _, sec := range res.sections {
		if !a.can(r, CapList, sec.child.name, "") {
			continue
		}
		v, err := a.relatedView(r, sec, key)
		if err != nil {
			LoggerFromContext(r.Context()).Warn("pgdesk: related rows could not be read",
				"resource", res.name, "related", sec.child.name, "error", err)
			continue
		}
		out = append(out, v)
	}
	return out
}

func (a *Admin) relatedView(r *http.Request, sec *relatedSection, key any) (relatedView, error) {
	child := sec.child
	scope, err := a.scopeFor(r, child, CapList, "")
	if err != nil {
		return relatedView{}, err
	}
	display := visibleColumns(sec.cols, child)
	fetch := append([]*introspect.Column(nil), display...)
	keyIdx := make([]int, 0, len(child.keyCols))
	for _, kc := range child.keyCols {
		idx := indexOfColumn(fetch, kc.Name)
		if idx < 0 {
			fetch = append(fetch, kc)
			idx = len(fetch) - 1
		}
		keyIdx = append(keyIdx, idx)
	}
	sql, args, err := query.BuildList(child.table, query.ListParams{
		Columns:  fetch,
		Filters:  withScope([]query.Filter{{Col: sec.fk, Op: query.OpEq, Values: []any{key}}}, scope),
		Sort:     sec.sort,
		SortDesc: sec.desc,
		Limit:    sec.limit + 1, // the extra row says there is more
	})
	if err != nil {
		return relatedView{}, err
	}
	ctx, cancel := a.queryContext(r)
	defer cancel()
	rows, err := a.runQuery(ctx, "related", sql, args)
	if err != nil {
		return relatedView{}, err
	}
	defer rows.Close()

	var cells [][]any
	var keys []string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return relatedView{}, err
		}
		seg := ""
		if child.hasKey() {
			keyVals := make([]any, len(keyIdx))
			for i, idx := range keyIdx {
				keyVals[i] = vals[idx]
			}
			if seg, err = query.EncodeKey(child.keyCols, keyVals); err != nil {
				return relatedView{}, err
			}
		}
		cells, keys = append(cells, vals[:len(display)]), append(keys, seg)
	}
	if err := rows.Err(); err != nil {
		return relatedView{}, err
	}

	v := relatedView{Title: sec.title, Resource: child.name, Limit: sec.limit, HasDetail: child.hasKey() && a.can(r, CapView, child.name, "")}
	if v.More = len(cells) > sec.limit; v.More {
		cells, keys = cells[:sec.limit], keys[:sec.limit]
		v.AllURL = a.relatedAllURL(child, sec.fk, key)
	}
	for _, c := range display {
		v.Headers = append(v.Headers, a.fieldLabel(child, c))
	}
	labels := a.resolveFKLabels(r, child, display, cells)
	v.Rows = make([]rowView, len(cells))
	for i, row := range cells {
		cv := make([]cellView, len(display))
		for ci, c := range display {
			cv[ci] = a.buildCell(child, c, row[ci], labels)
		}
		v.Rows[i] = rowView{Cells: cv, Key: keys[i]}
	}
	return v, nil
}

// relatedAllURL is empty unless the child filters this column by exact match (text filters are substring).
func (a *Admin) relatedAllURL(child *Resource, fk *introspect.Column, key any) string {
	if !containsColumn(child.filters, fk) {
		return ""
	}
	switch fk.Category {
	case introspect.CatText, introspect.CatEnum, introspect.CatBool, introspect.CatTimestamp:
		return ""
	}
	return a.listBase(child) + "?" + url.Values{"f_" + fk.Name: {fmt.Sprint(key)}}.Encode()
}

func containsColumn(cols []*introspect.Column, c *introspect.Column) bool {
	for _, x := range cols {
		if x.Name == c.Name {
			return true
		}
	}
	return false
}
