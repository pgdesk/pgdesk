package pgdesk

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
	"github.com/pgdesk/pgdesk/internal/render"
)

// indexOfColumn returns the position of a column by name, or -1.
func indexOfColumn(cols []*introspect.Column, name string) int {
	for i, c := range cols {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// parsePage parses a 1-based page number, defaulting to 1 and never below 1.
func parsePage(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// clampPageSize enforces the hard maximum page size regardless of request input
// (F6): a request can never force an unbounded scan or render.
func (a *Admin) clampPageSize(n int) int {
	if n <= 0 {
		n = a.cfg.defaultPageSize
	}
	if n > a.cfg.maxPageSize {
		return a.cfg.maxPageSize
	}
	return n
}

func (a *Admin) resourceMeta(res *Resource) resourceMeta {
	return resourceMeta{Name: res.name, Label: res.Label, LabelPlural: res.LabelPlural, Description: res.Description}
}

// fieldLabel returns the display label for a column: the operator override if
// set, else a humanized column name.
func (a *Admin) fieldLabel(res *Resource, c *introspect.Column) string {
	if fc := res.fields[c.Name]; fc != nil && fc.label != "" {
		return fc.label
	}
	return humanize(c.Name)
}

// buildFormFields turns columns + a row map into form fields, choosing widgets
// from type and applying readonly/required/error state. attemptedValues, when
// non-nil, overrides row values (used when re-rendering after a validation error
// so the operator keeps their input). fieldErrs maps column -> inline error.
func (a *Admin) buildFormFields(res *Resource, cols []*introspect.Column, row map[string]any, fieldErrs map[string]string) []formField {
	fields := make([]formField, 0, len(cols))
	for _, c := range cols {
		fc := res.fields[c.Name]
		// A generated or identity-always column is dropped from every write by
		// editableColumns/updatableColumns, so offering it as an input would
		// invite the operator to type a value that is then silently discarded.
		readonly := c.IsGenerated || c.IsIdentityAlways || (fc != nil && fc.readonly)
		val := row[c.Name]
		ff := formField{
			Name:        c.Name,
			Label:       a.fieldLabel(res, c),
			Value:       val,
			ValueString: render.FormatValue(val),
			Readonly:    readonly,
			// Nothing the operator cannot write is demanded of them: a readonly
			// input has no way to satisfy a required marker.
			Required: fc != nil && fc.required || (!c.Nullable && !c.HasDefault && !readonly),
			Widget:   string(widgetFor(c, fc)),
		}
		if ff.Widget == string(WidgetCheckbox) {
			ff.Checked = isTruthy(val)
		}
		if c.IsEnum() {
			ff.Options = c.EnumLabels
		}
		if fieldErrs != nil {
			ff.Error = fieldErrs[c.Name]
		}
		fields = append(fields, ff)
	}
	return fields
}

// widgetFor picks a form widget from the field override or the column type.
func widgetFor(c *introspect.Column, fc *fieldConfig) Widget {
	if fc != nil && fc.widget != WidgetAuto {
		return fc.widget
	}
	switch c.Category {
	case introspect.CatBool:
		return WidgetCheckbox
	case introspect.CatEnum:
		return WidgetSelect
	case introspect.CatJSON:
		return WidgetJSON
	case introspect.CatTimestamp:
		return WidgetDateTime
	default:
		return WidgetText
	}
}

func isTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "t" || x == "on"
	default:
		return false
	}
}

// formValueForColumn extracts a submitted value for a column and shapes it for
// pgx binding. Values travel as parameters ($N); PostgreSQL is the validator, so
// invalid text surfaces as a mapped 22P02 error rather than being pre-rejected
// here (D7). Empty non-text values become NULL so the DB enforces NOT NULL.
func formValueForColumn(r *http.Request, c *introspect.Column) any {
	if c.Category == introspect.CatBool {
		// A checkbox is present only when checked.
		return r.PostForm.Has(c.Name)
	}
	raw := r.PostFormValue(c.Name)
	if raw == "" {
		if c.Category == introspect.CatText {
			return ""
		}
		return nil
	}
	return raw
}

// scanOneRow reads at most one row into a column->value map, returning nil if the
// result set is empty (used to detect the O1 0-rows conflict).
func scanOneRow(rows pgx.Rows, cols []*introspect.Column) (map[string]any, error) {
	if !rows.Next() {
		return nil, rows.Err()
	}
	vals, err := rows.Values()
	if err != nil {
		return nil, err
	}
	m := make(map[string]any, len(cols))
	for i, c := range cols {
		if i < len(vals) {
			m[c.Name] = vals[i]
		}
	}
	return m, nil
}

// buildAuditEvent assembles a structured audit event, applying field-level
// redaction so secrets never enter the trail (O4).
func (a *Admin) buildAuditEvent(r *http.Request, res *Resource, action AuditAction, keyVals []any, before, after map[string]any) AuditEvent {
	actorID, actorName := "", ""
	if p := PrincipalFromContext(r.Context()); p != nil {
		actorID, actorName = p.SubjectID(), p.DisplayName()
	}
	keySeg, _ := query.EncodeKey(res.keyCols, keyVals)
	return AuditEvent{
		ActorID:   actorID,
		ActorName: actorName,
		Action:    action,
		Resource:  res.name,
		Key:       keySeg,
		Before:    a.redact(res, before),
		After:     a.redact(res, after),
		SourceIP:  clientIP(r),
		RequestID: RequestIDFromContext(r.Context()),
		At:        time.Now().UTC(),
	}
}

// buildAuditEventFromRow builds an audit event whose key is derived from a
// RETURNING row map (used for create, where the key is DB-generated and only
// known after the insert).
func (a *Admin) buildAuditEventFromRow(r *http.Request, res *Resource, action AuditAction, keyRow, before, after map[string]any) AuditEvent {
	keyVals := make([]any, 0, len(res.keyCols))
	for _, kc := range res.keyCols {
		keyVals = append(keyVals, keyRow[kc.Name])
	}
	return a.buildAuditEvent(r, res, action, keyVals, before, after)
}

// redact removes redacted columns from an audit snapshot (O4).
func (a *Admin) redact(res *Resource, snap map[string]any) map[string]any {
	if snap == nil {
		return nil
	}
	out := make(map[string]any, len(snap))
	for k, v := range snap {
		if fc := res.fields[k]; fc != nil && fc.redact {
			continue
		}
		out[k] = v
	}
	return out
}

// clientIP returns the remote address without the port. It does NOT trust
// X-Forwarded-For; the host's proxy/middleware should normalize RemoteAddr if it
// terminates TLS upstream (scope boundary).
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

// FormatVersion renders an optimistic-concurrency version token as a string for
// the hidden form field. xmin and an explicit version column are selected as
// ::text, so they arrive as a string. A NoVersion resource selects NULL, which
// decodes to nil and must render as the empty token -- never the "<nil>" that
// fmt.Sprint would produce and leak into the form.
func FormatVersion(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
