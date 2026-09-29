//go:build integration

package pgdesk_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgdesk/pgdesk"
)

// dbAudit writes each event to it_audit_log, in the change's transaction.
type dbAudit struct{}

func (dbAudit) LogAuditTx(ctx context.Context, tx pgx.Tx, e pgdesk.AuditEvent) error {
	_, err := tx.Exec(ctx,
		"INSERT INTO it_audit_log (action, resource, row_key, actor_id) VALUES ($1, $2, $3, $4)",
		string(e.Action), e.Resource, e.Key, e.ActorID)
	return err
}

// brokenAudit writes the event and then fails, like a sink that errors after a
// partial write. Neither the change nor the event may survive.
type brokenAudit struct{}

func (brokenAudit) LogAuditTx(ctx context.Context, tx pgx.Tx, e pgdesk.AuditEvent) error {
	if err := (dbAudit{}).LogAuditTx(ctx, tx, e); err != nil {
		return err
	}
	return errors.New("audit sink rejected the event")
}

func auditRows(t *testing.T, pool *pgxpool.Pool, action pgdesk.AuditAction) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM it_audit_log WHERE action = $1 AND resource = 'it_users' AND actor_id = 'tester'",
		string(action)).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

// auditedChange is a change an operator can make through the admin.
type auditedChange struct {
	name   string
	action pgdesk.AuditAction
	submit func(t *testing.T, admin *pgdesk.Admin, pool *pgxpool.Pool) *httptest.ResponseRecorder

	committed func(t *testing.T, pool *pgxpool.Pool) bool // whether the change reached the database
	failCode  int                                         // how a failed change is reported
	failMsg   string                                      // and what the operator is told
}

var auditedChanges = []auditedChange{
	{
		name:     "create",
		action:   pgdesk.AuditCreate,
		failCode: http.StatusUnprocessableEntity,
		failMsg:  "The change could not be saved. Please try again.",
		submit: func(t *testing.T, admin *pgdesk.Admin, _ *pgxpool.Pool) *httptest.ResponseRecorder {
			form := url.Values{"email": {"grace@example.com"}, "status": {"active"}}
			return postForm(admin, "/admin/it_users/new", createToken(t, admin), form)
		},
		committed: func(t *testing.T, pool *pgxpool.Pool) bool {
			return userExists(t, pool, "grace@example.com")
		},
	},
	{
		name:     "update",
		action:   pgdesk.AuditUpdate,
		failCode: http.StatusUnprocessableEntity,
		failMsg:  "The change could not be saved. Please try again.",
		submit: func(t *testing.T, admin *pgdesk.Admin, _ *pgxpool.Pool) *httptest.ResponseRecorder {
			token, version := editToken(t, admin, "1")
			form := url.Values{"email": {"ada.audited@example.com"}, "status": {"active"}}
			return postEdit(admin, "1", token, version, form)
		},
		committed: func(t *testing.T, pool *pgxpool.Pool) bool {
			return userExists(t, pool, "ada.audited@example.com")
		},
	},
	{
		name:     "delete",
		action:   pgdesk.AuditDelete,
		failCode: http.StatusInternalServerError,
		failMsg:  "An unexpected error occurred.",
		submit: func(t *testing.T, admin *pgdesk.Admin, _ *pgxpool.Pool) *httptest.ResponseRecorder {
			token, _ := editToken(t, admin, "1")
			return postForm(admin, "/admin/it_users/"+leavingID+"/delete", token, url.Values{})
		},
		committed: func(t *testing.T, pool *pgxpool.Pool) bool {
			return !userExists(t, pool, "leaving@example.com")
		},
	},
	{
		name:     "bulk action",
		action:   pgdesk.AuditBulkAction,
		failCode: http.StatusSeeOther,
		failMsg:  "The action could not be completed.",
		submit: func(t *testing.T, admin *pgdesk.Admin, pool *pgxpool.Pool) *httptest.ResponseRecorder {
			token, _ := editToken(t, admin, "1")
			alan := strconv.FormatInt(userID(t, pool, "alan@example.com"), 10)
			return postForm(admin, "/admin/it_users/action", token, url.Values{"_action": {"activate"}, "key": {alan}})
		},
		committed: func(t *testing.T, pool *pgxpool.Pool) bool {
			return userStatus(t, pool, "alan@example.com") == "active"
		},
	},
}

func TestIntegrationTxAuditCommitsWithTheChange(t *testing.T) {
	for _, c := range auditedChanges {
		t.Run(c.name, func(t *testing.T) {
			admin, pool := usersAdmin(t, pgdesk.WithTxAuditLogger(dbAudit{}))

			rec := c.submit(t, admin, pool)

			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303\n%s", rec.Code, rec.Body)
			}
			if !c.committed(t, pool) {
				t.Error("the change was not committed")
			}
			if n := auditRows(t, pool, c.action); n != 1 {
				t.Errorf("found %d %q audit rows, want 1", n, c.action)
			}
		})
	}
}

func TestIntegrationTxAuditFailureRollsBackTheChange(t *testing.T) {
	for _, c := range auditedChanges {
		t.Run(c.name, func(t *testing.T) {
			admin, pool := usersAdmin(t, pgdesk.WithTxAuditLogger(brokenAudit{}))

			rec := c.submit(t, admin, pool)

			if rec.Code != c.failCode {
				t.Errorf("status = %d, want %d", rec.Code, c.failCode)
			}
			if c.committed(t, pool) {
				t.Error("the change was committed although its audit event failed")
			}
			if n := auditRows(t, pool, c.action); n != 0 {
				t.Errorf("found %d %q audit rows for a change that did not happen, want 0", n, c.action)
			}
			body := rec.Body.String()
			if c.failCode == http.StatusSeeOther {
				body = followRedirect(t, admin, rec)
			}
			if !strings.Contains(body, c.failMsg) {
				t.Errorf("the operator is not told %q\n%s", c.failMsg, body)
			}
		})
	}
}
