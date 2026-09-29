package pgdesk

import (
	"net/http"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

func (a *Admin) renderFormWithErrors(w http.ResponseWriter, r *http.Request, res *Resource, keyVals []any, version string, me mappedError, scope []query.Filter) {
	display := visibleColumns(res.table.Columns(), res)
	current, _, err := a.fetchRow(r, res, display, keyVals, scope)
	if err != nil {
		current = map[string]any{}
	}
	fields := a.buildFormFields(r, res, display, current, me.fieldErrors)
	overlaySubmitted(r, res, fields)

	key := r.PathValue("key")
	data := formView{
		Base:      a.baseView(r, "Edit "+res.Label),
		Resource:  a.resourceMeta(res),
		Action:    a.editAction(res, key),
		Key:       key,
		Version:   version,
		FormError: withUnshownErrors(a, res, me, fields),
		Fields:    fields,
	}
	a.renderPage(w, r, http.StatusUnprocessableEntity, "form", data)
}

func withUnshownErrors(a *Admin, res *Resource, me mappedError, fields []formField) string {
	shown := make(map[string]bool, len(fields))
	for _, f := range fields {
		if !f.Readonly {
			shown[f.Name] = true
		}
	}
	msg := me.formError
	for _, c := range res.table.Columns() {
		e, ok := me.fieldErrors[c.Name]
		if !ok || shown[c.Name] {
			continue
		}
		line := a.fieldLabel(res, c) + " " + e + ", but it is not on this form."
		if msg == "" {
			msg = line
		} else {
			msg += " " + line
		}
	}
	return msg
}

func (a *Admin) renderConflict(w http.ResponseWriter, r *http.Request, res *Resource, keyVals []any, submittedVersion string, scope []query.Filter) {
	display := visibleColumns(res.table.Columns(), res)
	current, currentVersion, err := a.fetchRow(r, res, display, keyVals, scope)
	if err != nil {

		a.handleFetchError(w, r, err)
		return
	}
	fields := a.buildFormFields(r, res, display, current, nil)

	for i := range fields {
		fields[i].CurrentValue = current[fields[i].Name]
	}
	overlaySubmitted(r, res, fields)

	key := r.PathValue("key")
	data := formView{
		Base:     a.baseView(r, "Edit "+res.Label),
		Resource: a.resourceMeta(res),
		Action:   a.editAction(res, key),
		Key:      key,
		Version:  currentVersion,
		Conflict: true,
		Fields:   fields,
	}
	_ = submittedVersion
	a.renderPage(w, r, http.StatusConflict, "form", data)
}

func overlaySubmitted(r *http.Request, res *Resource, fields []formField) {
	editable := map[string]*introspect.Column{}
	for _, c := range res.editableColumns() {
		editable[c.Name] = c
	}
	for i := range fields {
		c, ok := editable[fields[i].Name]
		if !ok {
			continue
		}
		v := formValueForColumn(r, c)
		fields[i].Value = v
		fields[i].ValueString = valueString(v)
		if fields[i].Widget == string(WidgetCheckbox) {
			fields[i].Checked = isTruthy(v)
		}
	}
}

func valueString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return formatVersion(v)
}
