package pgdesk

import (
	"net/http"

	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

// renderFormWithErrors re-renders the edit form after a database validation
// error (D7), preserving the operator's submitted values and attaching inline
// field errors. Status 422 signals the input was well-formed but rejected.
func (a *Admin) renderFormWithErrors(w http.ResponseWriter, r *http.Request, res *Resource, keyVals []any, version string, me mappedError, scope []query.Filter) {
	display := visibleColumns(res.table.Columns(), res)
	current, _, err := a.fetchRow(r, res, display, keyVals, scope)
	if err != nil {
		current = map[string]any{} // row may be gone; still show the form
	}
	fields := a.buildFormFields(res, display, current, me.fieldErrors)
	overlaySubmitted(r, res, fields)

	key := r.PathValue("key")
	data := formView{
		Base:      a.baseView(r, "Edit "+res.Label),
		Resource:  a.resourceMeta(res),
		Action:    a.editAction(res, key),
		Key:       key,
		Version:   version,
		FormError: me.formError,
		Fields:    fields,
	}
	a.renderPage(w, r, http.StatusUnprocessableEntity, "form", data)
}

// renderConflict renders the O1 concurrent-edit conflict page: the operator's
// attempted values, the current DB values alongside, a clear notice, and the
// current version token so a resubmit can win. Status 409.
func (a *Admin) renderConflict(w http.ResponseWriter, r *http.Request, res *Resource, keyVals []any, submittedVersion string, scope []query.Filter) {
	display := visibleColumns(res.table.Columns(), res)
	current, currentVersion, err := a.fetchRow(r, res, display, keyVals, scope)
	if err != nil {
		// The row was deleted out from under the edit; treat as not found.
		a.handleFetchError(w, r, err)
		return
	}
	fields := a.buildFormFields(res, display, current, nil)
	// Show current DB value alongside, then overlay the operator's attempt as the
	// editable value so they can review and resubmit.
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
		Version:  currentVersion, // resubmitting against the current version wins
		Conflict: true,
		Fields:   fields,
	}
	_ = submittedVersion
	a.renderPage(w, r, http.StatusConflict, "form", data)
}

// overlaySubmitted replaces editable field values with the operator's submitted
// input so a re-rendered form keeps what they typed.
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
	return FormatVersion(v)
}
