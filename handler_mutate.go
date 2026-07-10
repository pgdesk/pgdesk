package pgdesk

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

// editAction and createAction build the form POST targets (absolute, under the
// base path -- F4 keeps redirects derived from these safe).
func (a *Admin) editAction(res *Resource, key string) string {
	return a.cfg.basePath + "/" + res.name + "/" + key + "/edit"
}

func (a *Admin) createAction(res *Resource) string {
	return a.cfg.basePath + "/" + res.name + "/new"
}

// dbError classifies a database error. A deadline (our per-request timeout, which
// also fires when the pool is saturated because acquisition respects the context)
// becomes a 503 with Retry-After rather than an indefinite hang or a 500 (O2).
// Everything else is a logged generic 500 (F5).
func (a *Admin) dbError(w http.ResponseWriter, r *http.Request, stage string, err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		LoggerFromContext(r.Context()).Warn("pgdesk: request deadline or pool saturation",
			"stage", stage, "error", err, "request_id", RequestIDFromContext(r.Context()))
		w.Header().Set("Retry-After", "1")
		a.renderError(w, r, http.StatusServiceUnavailable, "The server is busy. Please retry in a moment.")
		return
	}
	a.serverError(w, r, stage, err)
}

// newKeySegment extracts the resource's key values from a RETURNING row map and
// encodes them into a URL segment (D6), for redirecting to a newly-created row.
func newKeySegment(res *Resource, row map[string]any) (string, bool) {
	if !res.hasKey() || row == nil {
		return "", false
	}
	vals := make([]any, len(res.keyCols))
	for i, kc := range res.keyCols {
		v, ok := row[kc.Name]
		if !ok {
			return "", false
		}
		vals[i] = v
	}
	seg, err := query.EncodeKey(res.keyCols, vals)
	if err != nil {
		return "", false
	}
	return seg, true
}

// handleCreateForm renders an empty create form (O6 CapCreate; D6 requires the
// resource be writable).
func (a *Admin) handleCreateForm(w http.ResponseWriter, r *http.Request) {
	res, ok := a.liveResource(r, r.PathValue("resource"))
	if !ok || !res.canCreate() {
		a.renderError(w, r, http.StatusNotFound, "This resource cannot be created.")
		return
	}
	if !a.guard(w, r, CapCreate, res.name, "") {
		return
	}
	display := visibleColumns(res.table.Columns(), res)
	data := formView{
		Base:     a.baseView(r, "New "+res.Label),
		Resource: a.resourceMeta(res),
		Action:   a.createAction(res),
		IsCreate: true,
		Fields:   a.buildFormFields(res, display, map[string]any{}, nil),
	}
	a.renderPage(w, r, http.StatusOK, "form", data)
}

// handleCreate inserts a new row in a transaction with audit (O4), CSRF (D5), and
// pg-error mapping (D7), then redirects to the new row's detail page.
func (a *Admin) handleCreate(w http.ResponseWriter, r *http.Request) {
	res, ok := a.liveResource(r, r.PathValue("resource"))
	if !ok || !res.canCreate() {
		a.renderError(w, r, http.StatusNotFound, "This resource cannot be created.")
		return
	}
	if !a.guard(w, r, CapCreate, res.name, "") {
		return
	}
	scope, err := a.scopeFor(r, res, CapCreate, "")
	if err != nil {
		a.scopeDenied(w, r, res, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		a.renderError(w, r, http.StatusBadRequest, "The submitted form was invalid or too large.")
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		LoggerFromContext(r.Context()).Warn("pgdesk: CSRF verification failed", "resource", res.name, "error", err)
		a.renderError(w, r, http.StatusForbidden, "Your session could not be verified. Please reload and try again.")
		return
	}

	editable := res.editableColumns()
	setCols := make([]*introspect.Column, 0, len(editable))
	setVals := make([]any, 0, len(editable))
	for _, c := range editable {
		// Skip columns the operator left empty that have a default, so the DB
		// default (e.g. now(), nextval) applies instead of an explicit NULL.
		if !r.PostForm.Has(c.Name) && c.Category != introspect.CatBool {
			if c.HasDefault {
				continue
			}
		}
		setCols = append(setCols, c)
		setVals = append(setVals, formValueForColumn(r, c))
	}
	// Pin equality-scoped columns to their scope value so a scoped operator cannot
	// create a row outside their scope (O6).
	setCols, setVals = enforceScope(setCols, setVals, scope)
	returning := res.table.Columns()

	sql, args, err := query.InsertRow(res.table, setCols, setVals, returning)
	if err != nil {
		a.serverError(w, r, "build insert", err)
		return
	}

	ctx, cancel := a.queryContext(r)
	defer cancel()
	after, insErr := a.execInsertTx(ctx, r, res, sql, args, returning)
	if insErr != nil {
		me := mapPgError(insErr, res.constraintMsgs, res.table.UniqueColumns)
		if me.empty() {
			a.dbError(w, r, "create", insErr)
			return
		}
		a.renderCreateWithErrors(w, r, res, me)
		return
	}

	a.setFlash(w, "info", res.Label+" created.")
	if seg, ok := newKeySegment(res, after); ok {
		http.Redirect(w, r, a.safeRedirect(a.cfg.basePath+"/"+res.name+"/"+seg), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, a.safeRedirect(a.cfg.basePath+"/"+res.name), http.StatusSeeOther)
}

// execInsertTx runs the INSERT and audit in one transaction (O4).
func (a *Admin) execInsertTx(ctx context.Context, r *http.Request, res *Resource, sql string, args []any, returning []*introspect.Column) (map[string]any, error) {
	var after map[string]any
	err := a.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		after, err = scanOneRow(rows, returning)
		rows.Close()
		if err != nil {
			return err
		}
		if a.cfg.txAudit != nil {
			ev := a.buildAuditEventFromRow(r, res, AuditCreate, after, nil, after)
			if err := a.cfg.txAudit.LogAuditTx(ctx, tx, ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.bestEffortAudit(ctx, a.buildAuditEventFromRow(r, res, AuditCreate, after, nil, after))
	return after, nil
}

// renderCreateWithErrors re-renders the create form after a DB validation error
// (D7), preserving the operator's submitted values.
func (a *Admin) renderCreateWithErrors(w http.ResponseWriter, r *http.Request, res *Resource, me mappedError) {
	display := visibleColumns(res.table.Columns(), res)
	fields := a.buildFormFields(res, display, map[string]any{}, me.fieldErrors)
	overlaySubmitted(r, res, fields)
	data := formView{
		Base:      a.baseView(r, "New "+res.Label),
		Resource:  a.resourceMeta(res),
		Action:    a.createAction(res),
		IsCreate:  true,
		FormError: me.formError,
		Fields:    fields,
	}
	a.renderPage(w, r, http.StatusUnprocessableEntity, "form", data)
}

// handleDelete deletes a row in a transaction with audit (O4), CSRF (D5), then
// redirects to the list. A 0-row delete (already gone) is treated as success.
func (a *Admin) handleDelete(w http.ResponseWriter, r *http.Request) {
	res, ok := a.liveResource(r, r.PathValue("resource"))
	if !ok || !res.canDelete() {
		a.renderError(w, r, http.StatusNotFound, "This resource cannot be deleted.")
		return
	}
	if !a.guard(w, r, CapDelete, res.name, "") {
		return
	}
	scope, err := a.scopeFor(r, res, CapDelete, "")
	if err != nil {
		a.scopeDenied(w, r, res, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		a.renderError(w, r, http.StatusBadRequest, "The submitted form was invalid.")
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		LoggerFromContext(r.Context()).Warn("pgdesk: CSRF verification failed", "resource", res.name, "error", err)
		a.renderError(w, r, http.StatusForbidden, "Your session could not be verified. Please reload and try again.")
		return
	}
	keyVals, err := query.DecodeKey(res.keyCols, r.PathValue("key"))
	if err != nil {
		a.renderError(w, r, http.StatusBadRequest, "Invalid record identifier.")
		return
	}

	returning := res.table.Columns()
	sql, args, err := query.DeleteRow(res.table, res.keyCols, keyVals, returning, scope)
	if err != nil {
		a.serverError(w, r, "build delete", err)
		return
	}

	ctx, cancel := a.queryContext(r)
	defer cancel()
	if delErr := a.execDeleteTx(ctx, r, res, sql, args, returning, keyVals); delErr != nil {
		if errors.Is(delErr, errNotFound) {
			a.renderError(w, r, http.StatusNotFound, "Record not found.")
			return
		}
		me := mapPgError(delErr, res.constraintMsgs, res.table.UniqueColumns)
		if me.empty() {
			a.dbError(w, r, "delete", delErr)
			return
		}
		// A constraint (e.g. FK restrict) blocked the delete; surface on the detail.
		a.renderError(w, r, http.StatusConflict, "This record cannot be deleted because other records depend on it.")
		return
	}
	a.setFlash(w, "info", res.Label+" deleted.")
	http.Redirect(w, r, a.safeRedirect(a.cfg.basePath+"/"+res.name), http.StatusSeeOther)
}

// execDeleteTx runs the DELETE and audit in one transaction (O4). The RETURNING
// row is the before-snapshot. A 0-row delete means the row does not exist for this
// principal -- already gone, or outside their scope (O6) -- and reports errNotFound
// rather than a success the operator did not get. The two are deliberately
// indistinguishable so the response cannot be used to probe for rows.
func (a *Admin) execDeleteTx(ctx context.Context, r *http.Request, res *Resource, sql string, args []any, returning []*introspect.Column, keyVals []any) error {
	var before map[string]any
	err := a.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		before, err = scanOneRow(rows, returning)
		rows.Close()
		if err != nil {
			return err
		}
		if before == nil {
			return errNotFound
		}
		if a.cfg.txAudit != nil {
			ev := a.buildAuditEvent(r, res, AuditDelete, keyVals, before, nil)
			if err := a.cfg.txAudit.LogAuditTx(ctx, tx, ev); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if before != nil {
		a.bestEffortAudit(ctx, a.buildAuditEvent(r, res, AuditDelete, keyVals, before, nil))
	}
	return nil
}
