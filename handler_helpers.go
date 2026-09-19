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

func indexOfColumn(cols []*introspect.Column, name string) int {
	for i, c := range cols {
		if c.Name == name {
			return i
		}
	}
	return -1
}

func parsePage(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

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

func (a *Admin) fieldLabel(res *Resource, c *introspect.Column) string {
	if fc := res.fields[c.Name]; fc != nil && fc.label != "" {
		return fc.label
	}
	return humanize(c.Name)
}

func (a *Admin) buildFormFields(r *http.Request, res *Resource, cols []*introspect.Column, row map[string]any, fieldErrs map[string]string) []formField {

	fkLabels := a.resolveRowFKLabels(r, res, cols, row)
	fields := make([]formField, 0, len(cols))
	for _, c := range cols {
		fc := res.fields[c.Name]

		readonly := c.IsGenerated || c.IsIdentityAlways || (fc != nil && fc.readonly)
		val := row[c.Name]
		ff := formField{
			Name:        c.Name,
			Label:       a.fieldLabel(res, c),
			Value:       val,
			ValueString: render.FormatValue(val),
			Readonly:    readonly,

			Required: fc != nil && fc.required || (!c.Nullable && !c.HasDefault && !readonly),
			Widget:   string(widgetFor(c, fc)),
		}
		if ff.Widget == string(WidgetCheckbox) {
			ff.Checked = isTruthy(val)
		}
		if c.IsEnum() {
			ff.Options = c.EnumLabels
		}

		if ref, ok := a.resolveFKRef(r, res, c.Name); ok {
			ff.ValueLabel = fkLabels[c.Name].label
			if !readonly && (fc == nil || fc.widget == WidgetAuto) {
				ff.Widget = string(WidgetFK)
				ff.Ref = ref.resource.name
			}
		}
		if fieldErrs != nil {
			ff.Error = fieldErrs[c.Name]
		}
		fields = append(fields, ff)
	}
	return fields
}

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

func formValueForColumn(r *http.Request, c *introspect.Column) any {
	if c.Category == introspect.CatBool {

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

func (a *Admin) buildAuditEventFromRow(r *http.Request, res *Resource, action AuditAction, keyRow, before, after map[string]any) AuditEvent {
	keyVals := make([]any, 0, len(res.keyCols))
	for _, kc := range res.keyCols {
		keyVals = append(keyVals, keyRow[kc.Name])
	}
	return a.buildAuditEvent(r, res, action, keyVals, before, after)
}

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

func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}

func FormatVersion(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
