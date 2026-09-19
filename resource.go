package pgdesk

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

type Resource struct {
	Label       string
	LabelPlural string
	Description string

	name  string
	table *introspect.Table

	listDisplay  []*introspect.Column
	searchFields []*introspect.Column
	filters      []*introspect.Column
	sortCol      *introspect.Column
	sortDesc     bool
	pageSize     int

	keyCols    []*introspect.Column
	versionCol *introspect.Column
	labelCol   *introspect.Column

	fields         map[string]*fieldConfig
	constraintMsgs map[string]string
	middleware     []Middleware

	actions     map[string]*action
	actionOrder []string

	err error
}

func newResource(name string, t *introspect.Table, defaultPageSize int) *Resource {
	r := &Resource{
		name:           name,
		table:          t,
		Label:          humanize(name),
		LabelPlural:    humanize(name),
		pageSize:       defaultPageSize,
		keyCols:        t.PrimaryKey,
		fields:         map[string]*fieldConfig{},
		constraintMsgs: map[string]string{},
		actions:        map[string]*action{},
	}

	r.listDisplay = append(r.listDisplay, t.Columns()...)
	return r
}

func (r *Resource) fieldFor(name string) *fieldConfig {
	fc, ok := r.fields[name]
	if !ok {
		fc = &fieldConfig{}
		r.fields[name] = fc
	}
	return fc
}

func (r *Resource) resolve(method string, names ...string) ([]*introspect.Column, bool) {
	cols := make([]*introspect.Column, 0, len(names))
	for _, n := range names {
		c, err := query.ResolveColumn(r.table, n)
		if err != nil {
			r.addErr(fmt.Errorf("%w: %q on table %q (referenced by %s)", ErrUnknownColumn, n, r.name, method))
			return nil, false
		}
		cols = append(cols, c)
	}
	return cols, true
}

func (r *Resource) addErr(err error) {
	r.err = errors.Join(r.err, err)
}

func (r *Resource) ListDisplay(cols ...string) {
	if resolved, ok := r.resolve("ListDisplay", cols...); ok {
		r.listDisplay = resolved
	}
}

func (r *Resource) SearchFields(cols ...string) {
	if resolved, ok := r.resolve("SearchFields", cols...); ok {
		r.searchFields = resolved
	}
}

func (r *Resource) Filters(cols ...string) {
	if resolved, ok := r.resolve("Filters", cols...); ok {
		r.filters = resolved
	}
}

func (r *Resource) Readonly(cols ...string) {
	for _, n := range cols {
		if _, ok := r.resolve("Readonly", n); ok {
			r.fieldFor(n).readonly = true
		}
	}
}

func (r *Resource) Hidden(cols ...string) {
	for _, n := range cols {
		if _, ok := r.resolve("Hidden", n); ok {
			r.fieldFor(n).hidden = true
		}
	}
}

func (r *Resource) Required(cols ...string) {
	for _, n := range cols {
		if _, ok := r.resolve("Required", n); ok {
			r.fieldFor(n).required = true
		}
	}
}

func (r *Resource) Redact(cols ...string) {
	for _, n := range cols {
		if _, ok := r.resolve("Redact", n); ok {
			r.fieldFor(n).redact = true
		}
	}
}

func (r *Resource) FieldLabel(col, label string) {
	if _, ok := r.resolve("FieldLabel", col); ok {
		r.fieldFor(col).label = label
	}
}

func (r *Resource) Widget(col string, w Widget) {
	if _, ok := r.resolve("Widget", col); ok {
		r.fieldFor(col).widget = w
	}
}

func (r *Resource) DefaultSort(spec string) {
	desc := false
	name := spec
	if strings.HasPrefix(spec, "-") {
		desc = true
		name = spec[1:]
	}
	if resolved, ok := r.resolve("DefaultSort", name); ok {
		r.sortCol = resolved[0]
		r.sortDesc = desc
	}
}

func (r *Resource) PageSize(n int) {
	if n > 0 {
		r.pageSize = n
	}
}

func (r *Resource) Key(cols ...string) {
	if resolved, ok := r.resolve("Key", cols...); ok {
		r.keyCols = resolved
	}
}

func (r *Resource) VersionColumn(col string) {
	if resolved, ok := r.resolve("VersionColumn", col); ok {
		r.versionCol = resolved[0]
	}
}

func (r *Resource) LabelColumn(col string) {
	if resolved, ok := r.resolve("LabelColumn", col); ok {
		r.labelCol = resolved[0]
	}
}

func (r *Resource) ConstraintMessage(name, msg string) {
	r.constraintMsgs[name] = msg
}

func (r *Resource) Use(mw ...Middleware) {
	r.middleware = append(r.middleware, mw...)
}

func (r *Resource) Action(name, label string, fn ActionFunc, opts ...ActionOption) {
	if !isURLSafe(name) {
		r.addErr(fmt.Errorf("action name %q must be url-safe (letters, digits, - or _)", name))
		return
	}
	if fn == nil {
		r.addErr(fmt.Errorf("action %q has a nil function", name))
		return
	}
	if !r.hasKey() {
		r.addErr(fmt.Errorf("action %q requires a keyed resource", name))
		return
	}
	act := &action{name: name, label: label, fn: fn}
	for _, o := range opts {
		o(act)
	}
	if _, exists := r.actions[name]; !exists {
		r.actionOrder = append(r.actionOrder, name)
	}
	r.actions[name] = act
}

func isURLSafe(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

func (r *Resource) hasKey() bool { return len(r.keyCols) > 0 }

func (r *Resource) writable() bool {
	return r.hasKey() && (r.table.Insertable || r.table.Updatable || r.table.Deletable)
}

func (r *Resource) canCreate() bool { return r.hasKey() && r.table.Insertable }

func (r *Resource) canUpdate() bool {
	return r.hasKey() && r.table.Updatable && len(r.updatableColumns()) > 0
}

func (r *Resource) canDelete() bool { return r.hasKey() && r.table.Deletable }

func (r *Resource) versionStrategy() query.VersionStrategy {
	switch {
	case r.versionCol != nil:
		return query.VersionStrategy{Column: r.versionCol}
	case r.table.HasXmin:
		return query.VersionStrategy{}
	default:
		return query.VersionStrategy{NoVersion: true}
	}
}

func (r *Resource) editableColumns() []*introspect.Column {
	var out []*introspect.Column
	for _, c := range r.table.Columns() {
		fc := r.fields[c.Name]
		if fc != nil && (fc.readonly || fc.hidden) {
			continue
		}
		if c.IsGenerated || c.IsIdentityAlways {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (r *Resource) updatableColumns() []*introspect.Column {
	key := make(map[string]bool, len(r.keyCols))
	for _, c := range r.keyCols {
		key[c.Name] = true
	}
	var out []*introspect.Column
	for _, c := range r.editableColumns() {
		if key[c.Name] {
			continue
		}
		out = append(out, c)
	}
	return out
}

func humanize(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
