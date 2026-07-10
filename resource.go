package pgdesk

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

// Resource is a table or view plus its admin behavior. It is configured by the
// callback passed to Admin.Resource, which runs once at New() against the
// immutable catalog. Setter methods that reference columns validate against the
// catalog and fail the whole construction if a name is unknown (D2, fail-closed).
//
// Capabilities are derived, not assumed (D6): detail/edit/delete exist only when
// the resource has a usable key AND the underlying relation is updatable.
type Resource struct {
	// Label and LabelPlural are free-form display strings; plain fields are fine
	// because they are not validated against the catalog.
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

	keyCols    []*introspect.Column // defaults to table.PrimaryKey; override via Key
	versionCol *introspect.Column   // nil -> xmin (O1)

	fields         map[string]*fieldConfig
	constraintMsgs map[string]string
	middleware     []Middleware

	actions     map[string]*action
	actionOrder []string

	err error // accumulated configuration errors (errors.Join); checked by New()
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
	// Sensible default list: all columns in ordinal order.
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

// resolve turns column names into catalog columns, recording every failure on
// the resource so New() reports all configuration mistakes at once (D2).
func (r *Resource) resolve(names ...string) ([]*introspect.Column, bool) {
	cols := make([]*introspect.Column, 0, len(names))
	for _, n := range names {
		c, err := query.ResolveColumn(r.table, n)
		if err != nil {
			r.addErr(fmt.Errorf("%w: %q on table %q", ErrUnknownColumn, n, r.name))
			return nil, false
		}
		cols = append(cols, c)
	}
	return cols, true
}

// addErr accumulates a configuration error. All errors are joined so a resource
// with several bad column names surfaces them together, rather than one per
// rebuild. errors.Is/As traverse the join, so callers matching a sentinel still
// work.
func (r *Resource) addErr(err error) {
	r.err = errors.Join(r.err, err)
}

// ListDisplay sets the columns shown in the list view, in order. Unknown columns
// fail construction.
func (r *Resource) ListDisplay(cols ...string) {
	if resolved, ok := r.resolve(cols...); ok {
		r.listDisplay = resolved
	}
}

// SearchFields sets the columns matched by the search box (text columns). Unknown
// columns fail construction.
func (r *Resource) SearchFields(cols ...string) {
	if resolved, ok := r.resolve(cols...); ok {
		r.searchFields = resolved
	}
}

// Filters sets the columns exposed as filters. Each column's allowed operators
// are derived from its type category (D3).
func (r *Resource) Filters(cols ...string) {
	if resolved, ok := r.resolve(cols...); ok {
		r.filters = resolved
	}
}

// Readonly marks columns as non-editable in forms (still shown). Unknown columns
// fail construction.
func (r *Resource) Readonly(cols ...string) {
	for _, n := range cols {
		if _, ok := r.resolve(n); ok {
			r.fieldFor(n).readonly = true
		}
	}
}

// Hidden hides columns from both list and form views.
func (r *Resource) Hidden(cols ...string) {
	for _, n := range cols {
		if _, ok := r.resolve(n); ok {
			r.fieldFor(n).hidden = true
		}
	}
}

// Required marks columns as required in the form as UX pre-flight only; the
// database remains the authority (D7).
func (r *Resource) Required(cols ...string) {
	for _, n := range cols {
		if _, ok := r.resolve(n); ok {
			r.fieldFor(n).required = true
		}
	}
}

// Redact omits a column's value from audit before/after snapshots (O4).
func (r *Resource) Redact(cols ...string) {
	for _, n := range cols {
		if _, ok := r.resolve(n); ok {
			r.fieldFor(n).redact = true
		}
	}
}

// FieldLabel overrides the display label for a single column in list and form
// views. Without it a column falls back to a humanized version of its name.
// Unknown columns fail construction.
func (r *Resource) FieldLabel(col, label string) {
	if _, ok := r.resolve(col); ok {
		r.fieldFor(col).label = label
	}
}

// Widget overrides the form input rendered for a column, instead of the default
// chosen from the column's type. Use it to force, for example, a Textarea on a
// long text column or the JSON editor on a jsonb column. Unknown columns fail
// construction.
func (r *Resource) Widget(col string, w Widget) {
	if _, ok := r.resolve(col); ok {
		r.fieldFor(col).widget = w
	}
}

// DefaultSort sets the default ordering. A leading '-' means descending
// (e.g. "-created_at"). The column must exist.
func (r *Resource) DefaultSort(spec string) {
	desc := false
	name := spec
	if strings.HasPrefix(spec, "-") {
		desc = true
		name = spec[1:]
	}
	if resolved, ok := r.resolve(name); ok {
		r.sortCol = resolved[0]
		r.sortDesc = desc
	}
}

// PageSize sets the default page size for the list view. It is still clamped to
// the admin's hard maximum at request time (F6).
func (r *Resource) PageSize(n int) {
	if n > 0 {
		r.pageSize = n
	}
}

// Key overrides the resource's key columns. Use it for views or unkeyed tables
// to enable detail/edit/delete (D6). Unknown columns fail construction.
func (r *Resource) Key(cols ...string) {
	if resolved, ok := r.resolve(cols...); ok {
		r.keyCols = resolved
	}
}

// WithVersionColumn uses an explicit version/updated_at column for optimistic
// concurrency instead of xmin (O1). The column must exist.
func (r *Resource) WithVersionColumn(col string) {
	if resolved, ok := r.resolve(col); ok {
		r.versionCol = resolved[0]
	}
}

// WithConstraintMessage maps a PostgreSQL constraint name to a friendly message
// shown when that constraint is violated (D7).
func (r *Resource) WithConstraintMessage(name, msg string) {
	r.constraintMsgs[name] = msg
}

// Use adds middleware that wraps only this resource's routes.
func (r *Resource) Use(mw ...Middleware) {
	r.middleware = append(r.middleware, mw...)
}

// Action registers a row/bulk action. name must be URL-safe and unique on the
// resource; label is shown in the action menu; fn runs against the selected rows
// inside a transaction (O4). Registering an action requires the resource be
// keyed (it operates on selected primary keys) -- otherwise construction fails.
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

// isURLSafe reports whether s can be used as a URL path segment. Resource and
// action names both become route segments, so both must satisfy it.
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

// --- derived capabilities (D6) ---

// hasKey reports whether detail/edit/delete routes should exist.
func (r *Resource) hasKey() bool { return len(r.keyCols) > 0 }

// writable reports whether ANY mutation (create, update, or delete) is possible:
// the resource must have a key and the relation must support at least one write
// operation. It gates the CSRF-key requirement and the general edit affordance;
// the specific create/update/delete gates below decide each operation precisely
// so a view that supports only some of them exposes exactly those (D6).
func (r *Resource) writable() bool {
	return r.hasKey() && (r.table.Insertable || r.table.Updatable || r.table.Deletable)
}

// canCreate reports whether INSERT is possible for this resource.
func (r *Resource) canCreate() bool { return r.hasKey() && r.table.Insertable }

// canUpdate reports whether UPDATE is possible for this resource: it has a key,
// the relation is updatable, and there is at least one non-key column to write
// (a table whose only columns are its key has nothing an edit could change).
func (r *Resource) canUpdate() bool {
	return r.hasKey() && r.table.Updatable && len(r.updatableColumns()) > 0
}

// canDelete reports whether DELETE is possible for this resource.
func (r *Resource) canDelete() bool { return r.hasKey() && r.table.Deletable }

// versionStrategy returns the O1 concurrency strategy for this resource. An
// explicit version column wins; otherwise xmin is used on physical relations
// (ordinary/partitioned tables, matviews) and versioning is disabled on views and
// foreign tables, which have no selectable xmin (using it there would 500).
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

// editableColumns returns the columns an operator may submit on CREATE: visible,
// not readonly, not generated, and not a GENERATED ALWAYS AS IDENTITY column.
// Identity-always columns are excluded because PostgreSQL rejects writing any
// value but DEFAULT to them (SQLSTATE 428C9); including one would make every
// insert fail. GENERATED BY DEFAULT AS IDENTITY columns are writable and remain
// editable, as are natural key columns (a natural key must be settable on insert).
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

// updatableColumns returns the columns an operator may submit on UPDATE: the
// creatable set minus the resource's key columns. A primary key is never rewritten
// on edit -- it identifies the row in the WHERE clause, and writing it back is at
// best a no-op and at worst an error (an identity or serial surrogate key, or a
// view column mapping onto one, rejects the write with SQLSTATE 428C9). Excluding
// the key from the SET list is what makes identity-PK tables and keyed updatable
// views editable by default.
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

// humanize turns a snake_case identifier into a Title Cased label.
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
