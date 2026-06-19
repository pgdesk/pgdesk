package pgdesk

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pgdesk/pgdesk/internal/query"
)

// handleAction runs a row/bulk action against the operator's selected rows,
// transactionally and audited (O4). It inherits every backend contract: CSRF
// (D5), central authorization (O6, CapRunAction), type-aware key decoding (D6),
// and the bulk-selection cap (F6).
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
	if err := a.verifyCSRF(r); err != nil {
		LoggerFromContext(r.Context()).Warn("pgdesk: CSRF verification failed", "resource", res.name, "error", err)
		a.renderError(w, r, http.StatusForbidden, "Your session could not be verified. Please reload and try again.")
		return
	}

	act, ok := res.actions[r.PostFormValue("_action")]
	if !ok {
		a.renderError(w, r, http.StatusBadRequest, "Unknown action.")
		return
	}
	if !a.guard(w, r, CapRunAction, res.name, act.name, a.authorizerFor(res)) {
		return
	}
	if act.allowed != nil && !act.allowed(PrincipalFromContext(r.Context())) {
		a.renderError(w, r, http.StatusForbidden, "You are not permitted to run this action.")
		return
	}

	// Decode the selected keys, fail-closed, capped (F6).
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

	msg, runErr := a.execActionTx(ctx, r, res, act, keys)
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

// execActionTx runs the action and its audit record in one transaction (O4).
func (a *Admin) execActionTx(ctx context.Context, r *http.Request, res *Resource, act *action, keys [][]any) (string, error) {
	var msg string
	err := a.withTx(ctx, func(tx pgx.Tx) error {
		m, err := act.fn(ctx, tx, keys)
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

// buildActionAuditEvent records a bulk-action audit event (O4): actor, action,
// resource, and the count/keys affected.
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
