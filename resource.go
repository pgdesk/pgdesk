package pgdesk

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

// Resource configures how one table is listed and edited.
type Resource struct {
	// Label is the singular name. LabelPlural is the plural. Both default from the table name.
	Label       string
	LabelPlural string
	// Description is shown under the list title.
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
		Label:          singular(humanize(name)),
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

// ListDisplay sets the list columns, in order. Default: all columns.
func (r *Resource) ListDisplay(cols ...string) {
	if resolved, ok := r.resolve("ListDisplay", cols...); ok {
		r.listDisplay = resolved
	}
}

// SearchFields enables case-insensitive substring search over cols. A single-column
// foreign key matches the referenced row's label.
func (r *Resource) SearchFields(cols ...string) {
	resolved, ok := r.resolve("SearchFields", cols...)
	if !ok {
		return
	}
	for _, c := range resolved {
		if !searchable(r, c) {
			r.addErr(fmt.Errorf("%w: %q on table %q is %s", ErrUnsearchableColumn, c.Name, r.name, c.DataType))
			return
		}
	}
	r.searchFields = resolved
}

// Filters sets the list filters, one per column.
func (r *Resource) Filters(cols ...string) {
	if resolved, ok := r.resolve("Filters", cols...); ok {
		r.filters = resolved
	}
}

// Readonly shows cols in forms without letting them be edited.
func (r *Resource) Readonly(cols ...string) {
	for _, n := range cols {
		if _, ok := r.resolve("Readonly", n); ok {
			r.fieldFor(n).readonly = true
		}
	}
}

// Hidden removes cols from lists, detail pages, forms and CSV export.
func (r *Resource) Hidden(cols ...string) {
	for _, n := range cols {
		if _, ok := r.resolve("Hidden", n); ok {
			r.fieldFor(n).hidden = true
		}
	}
}

// Required marks cols as required in forms.
func (r *Resource) Required(cols ...string) {
	for _, n := range cols {
		if _, ok := r.resolve("Required", n); ok {
			r.fieldFor(n).required = true
		}
	}
}

// Redact leaves cols out of AuditEvent.Before and AuditEvent.After.
func (r *Resource) Redact(cols ...string) {
	for _, n := range cols {
		if _, ok := r.resolve("Redact", n); ok {
			r.fieldFor(n).redact = true
		}
	}
}

// FieldLabel sets the label shown for col.
func (r *Resource) FieldLabel(col, label string) {
	if _, ok := r.resolve("FieldLabel", col); ok {
		r.fieldFor(col).label = label
	}
}

// Widget sets the form input for col.
func (r *Resource) Widget(col string, w Widget) {
	if _, ok := r.resolve("Widget", col); ok {
		r.fieldFor(col).widget = w
	}
}

// DefaultSort sets the initial list sort column. Prefix it with - for descending.
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

// PageSize sets the default rows per list page. Default 50.
func (r *Resource) PageSize(n int) {
	if n > 0 {
		r.pageSize = n
	}
}

// Key sets the columns that identify a row. Default: the primary key.
func (r *Resource) Key(cols ...string) {
	if resolved, ok := r.resolve("Key", cols...); ok {
		r.keyCols = resolved
	}
}

// VersionColumn sets the column that detects concurrent edits. Default: the row's xmin, where it exists.
func (r *Resource) VersionColumn(col string) {
	if resolved, ok := r.resolve("VersionColumn", col); ok {
		r.versionCol = resolved[0]
	}
}

// LabelColumn sets the column that names this table's rows where other tables reference them.
// Default: the first visible text column, else the key.
func (r *Resource) LabelColumn(col string) {
	if resolved, ok := r.resolve("LabelColumn", col); ok {
		r.labelCol = resolved[0]
	}
}

// ConstraintMessage sets the error shown when the named constraint rejects a change.
func (r *Resource) ConstraintMessage(name, msg string) {
	r.constraintMsgs[name] = msg
}

// Use adds middleware to this resource's routes.
func (r *Resource) Use(mw ...Middleware) {
	r.middleware = append(r.middleware, mw...)
}

// Action adds a bulk action that runs fn on the selected rows.
// name must be URL-safe; label is shown on the button.
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

var acronyms = map[string]string{
	"id": "ID", "ids": "IDs", "api": "API", "url": "URL", "urls": "URLs", "uuid": "UUID",
	"ip": "IP", "oauth": "OAuth", "json": "JSON", "sms": "SMS", "http": "HTTP", "sso": "SSO", "otp": "OTP",
}

func humanize(s string) string {
	parts := strings.Split(s, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		if a, ok := acronyms[strings.ToLower(p)]; ok {
			parts[i] = a
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func singular(label string) string {
	head, word := "", label
	if i := strings.LastIndex(label, " "); i >= 0 {
		head, word = label[:i+1], label[i+1:]
	}
	lower := strings.ToLower(word)
	switch {
	case strings.HasSuffix(lower, "ies") && len(word) > 3:
		word = word[:len(word)-3] + "y"
	case strings.HasSuffix(lower, "sses"), strings.HasSuffix(lower, "shes"), strings.HasSuffix(lower, "ches"), strings.HasSuffix(lower, "xes"):
		word = word[:len(word)-2]
	case strings.HasSuffix(lower, "s") && !strings.HasSuffix(lower, "ss") && !strings.HasSuffix(lower, "us") && !strings.HasSuffix(lower, "is"):
		word = word[:len(word)-1]
	}
	return head + word
}
