package pgdesk

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/pgdesk/pgdesk/internal/introspect"
	"github.com/pgdesk/pgdesk/internal/query"
)

func (a *Admin) editAction(res *Resource, key string) string {
	return a.cfg.basePath + "/" + res.name + "/" + key + "/edit"
}

func (a *Admin) createAction(res *Resource) string {
	return a.cfg.basePath + "/" + res.name + "/new"
}

func isDeadline(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

func (a *Admin) dbError(w http.ResponseWriter, r *http.Request, stage string, err error) {
	if isDeadline(err) {
		LoggerFromContext(r.Context()).Warn("pgdesk: request deadline or pool saturation",
			"stage", stage, "error", err, "request_id", RequestIDFromContext(r.Context()))
		w.Header().Set("Retry-After", "1")
		a.renderError(w, r, http.StatusServiceUnavailable, "The server is busy. Please retry in a moment.")
		return
	}
	a.serverError(w, r, stage, err)
}

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
		Fields:   a.buildFormFields(r, res, display, map[string]any{}, nil),
	}
	a.renderPage(w, r, http.StatusOK, "form", data)
}

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

		if !r.PostForm.Has(c.Name) && c.Category != introspect.CatBool {
			if c.HasDefault {
				continue
			}
		}
		setCols = append(setCols, c)
		setVals = append(setVals, formValueForColumn(r, c))
	}

	setCols, setVals = enforceScope(setCols, setVals, scope)
	returning := res.table.Columns()

	sql, args, err := query.InsertRow(res.table, setCols, setVals, returning)
	if err != nil {
		a.serverError(w, r, "build insert", err)
		return
	}

	ctx, cancel := a.queryContext(r)
	defer cancel()
	after, insErr := a.execInsertTx(ctx, r, res, sql, args, returning, setCols, setVals)
	if insErr != nil {
		var fkErr *fkScopeError
		if errors.As(insErr, &fkErr) {
			a.renderCreateWithErrors(w, r, res, unavailableSelection(fkErr))
			return
		}
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

func (a *Admin) execInsertTx(ctx context.Context, r *http.Request, res *Resource, sql string, args []any, returning []*introspect.Column, setCols []*introspect.Column, setVals []any) (map[string]any, error) {
	var after map[string]any
	err := a.withTx(ctx, func(tx pgx.Tx) error {

		if err := a.checkFKScope(ctx, tx, r, res, setCols, setVals); err != nil {
			return err
		}
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

func unavailableSelection(e *fkScopeError) mappedError {
	return mappedError{fieldErrors: map[string]string{e.column: "is not an available selection"}}
}

func (a *Admin) renderCreateWithErrors(w http.ResponseWriter, r *http.Request, res *Resource, me mappedError) {
	display := visibleColumns(res.table.Columns(), res)
	fields := a.buildFormFields(r, res, display, map[string]any{}, me.fieldErrors)
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

		a.renderError(w, r, http.StatusConflict, "This record cannot be deleted because other records depend on it.")
		return
	}
	a.setFlash(w, "info", res.Label+" deleted.")
	http.Redirect(w, r, a.safeRedirect(a.cfg.basePath+"/"+res.name), http.StatusSeeOther)
}

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
