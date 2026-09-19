package pgdesk

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pgdesk/pgdesk/internal/query"
)

func (a *Admin) handleAction(w http.ResponseWriter, r *http.Request) {
	res, ok := a.liveResource(r, r.PathValue("resource"))
	if !ok {
		a.renderError(w, r, http.StatusNotFound, "Unknown resource.")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		a.renderError(w, r, http.StatusBadRequest, "The submitted form was invalid or too large.")
		return
	}

	act, ok := res.actions[r.PostFormValue("_action")]
	if !ok {
		a.renderError(w, r, http.StatusBadRequest, "Unknown action.")
		return
	}
	if !a.guard(w, r, CapRunAction, res.name, act.name) {
		return
	}
	scope, err := a.scopeFor(r, res, CapRunAction, act.name)
	if err != nil {
		a.scopeDenied(w, r, res, err)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		LoggerFromContext(r.Context()).Warn("pgdesk: CSRF verification failed", "resource", res.name, "error", err)
		a.renderError(w, r, http.StatusForbidden, "Your session could not be verified. Please reload and try again.")
		return
	}

	segs := r.PostForm["key"]
	if len(segs) == 0 {
		a.setFlash(w, "error", "No rows were selected.")
		http.Redirect(w, r, a.safeRedirect(a.cfg.basePath+"/"+res.name), http.StatusSeeOther)
		return
	}
	if len(segs) > a.cfg.maxBulk {
		a.renderError(w, r, http.StatusBadRequest,
			fmt.Sprintf("Too many rows selected (max %d).", a.cfg.maxBulk))
		return
	}
	keys := make([][]any, 0, len(segs))
	for _, seg := range segs {
		kv, err := query.DecodeKey(res.keyCols, seg)
		if err != nil {
			a.renderError(w, r, http.StatusBadRequest, "Invalid record identifier.")
			return
		}
		keys = append(keys, kv)
	}

	ctx, cancel := a.queryContext(r)
	defer cancel()

	msg, runErr := a.execActionTx(ctx, r, res, act, keys, scope)
	if runErr != nil {
		LoggerFromContext(r.Context()).Warn("pgdesk: action failed",
			"resource", res.name, "action", act.name, "error", runErr)
		a.setFlash(w, "error", "The action could not be completed.")
		http.Redirect(w, r, a.safeRedirect(a.cfg.basePath+"/"+res.name), http.StatusSeeOther)
		return
	}
	if msg == "" {
		msg = fmt.Sprintf("%s applied to %d row(s).", act.label, len(keys))
	}
	a.setFlash(w, "info", msg)
	http.Redirect(w, r, a.safeRedirect(a.cfg.basePath+"/"+res.name), http.StatusSeeOther)
}

func (a *Admin) vetActionKeys(ctx context.Context, tx pgx.Tx, res *Resource, keys [][]any, scope []query.Filter) error {
	sql, args, err := query.CountRowsInScope(res.table, res.keyCols, keys, scope)
	if err != nil {
		return err
	}
	var n int64
	if err := tx.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		return err
	}
	if n != int64(len(keys)) {
		return fmt.Errorf("%w: only %d of %d selected rows are visible to this principal", errNotFound, n, len(keys))
	}
	return nil
}

func (a *Admin) execActionTx(ctx context.Context, r *http.Request, res *Resource, act *action, keys [][]any, scope []query.Filter) (string, error) {
	var msg string
	err := a.withTx(ctx, func(tx pgx.Tx) error {
		if err := a.vetActionKeys(ctx, tx, res, keys, scope); err != nil {
			return err
		}
		m, err := act.fn(ctx, tx, Keys{cols: res.keyCols, vals: keys})
		if err != nil {
			return err
		}
		msg = m
		if a.cfg.txAudit != nil {
			if err := a.cfg.txAudit.LogAuditTx(ctx, tx, a.buildActionAuditEvent(r, res, act, keys)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	a.bestEffortAudit(ctx, a.buildActionAuditEvent(r, res, act, keys))
	return msg, nil
}

func (a *Admin) buildActionAuditEvent(r *http.Request, res *Resource, act *action, keys [][]any) AuditEvent {
	actorID, actorName := "", ""
	if p := PrincipalFromContext(r.Context()); p != nil {
		actorID, actorName = p.SubjectID(), p.DisplayName()
	}
	return AuditEvent{
		ActorID:    actorID,
		ActorName:  actorName,
		Action:     AuditBulkAction,
		Resource:   res.name,
		ActionName: act.name,
		Key:        fmt.Sprintf("%d row(s)", len(keys)),
		SourceIP:   clientIP(r),
		RequestID:  RequestIDFromContext(r.Context()),
		At:         time.Now().UTC(),
	}
}
