package pgdesk

import (
	"net/http"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

type searchRef struct {
	col *introspect.Column
	ref fkRef
}

func (a *Admin) searchRefs(r *http.Request, res *Resource) []searchRef {
	var out []searchRef
	for _, c := range res.searchFields {
		ref, ok := a.resolveFKRef(r, res, c.Name)
		if !ok || ref.label.Category != introspect.CatText || concealed(ref.resource, ref.label) {
			continue
		}
		out = append(out, searchRef{col: c, ref: ref})
	}
	return out
}

func (a *Admin) searchEnabled(r *http.Request, res *Resource) bool {
	return len(textColumns(res.searchFields)) > 0 || len(a.searchRefs(r, res)) > 0
}

func (a *Admin) withRefSearch(r *http.Request, res *Resource, spec *query.SearchSpec) (*query.SearchSpec, error) {
	if spec == nil {
		return nil, nil
	}
	refs := a.searchRefs(r, res)
	if len(refs) == 0 {
		return spec, nil
	}
	out := &query.SearchSpec{Cols: spec.Cols, Term: spec.Term, Refs: make([]query.RefSearch, 0, len(refs))}
	for _, sr := range refs {
		scope, err := a.scopeFor(r, sr.ref.resource, CapView, "")
		if err != nil {
			return nil, err
		}
		out.Refs = append(out.Refs, query.RefSearch{
			Col:   sr.col,
			Table: sr.ref.table,
			Key:   sr.ref.pk,
			Label: sr.ref.label,
			Scope: scope,
		})
	}
	return out, nil
}

func concealed(res *Resource, c *introspect.Column) bool {
	fc := res.fields[c.Name]
	return fc != nil && (fc.hidden || fc.redact)
}

func searchable(res *Resource, c *introspect.Column) bool {
	return c.Category == introspect.CatText || singleColumnFK(res.table, c.Name) != nil
}
