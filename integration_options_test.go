//go:build integration

package pgdesk_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pgdesk/pgdesk"
)

const docsSchema = `
CREATE TABLE it_docs (
    id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title    text NOT NULL,
    revision int  NOT NULL DEFAULT 1
);
INSERT INTO it_docs (title) VALUES ('unpublished');
CREATE VIEW it_draft_docs AS SELECT id, title FROM it_docs WHERE revision = 1;
`

// editableUsers leaves email and status as the edit form's required inputs.
func editableUsers(r *pgdesk.Resource) { r.Readonly("created_at") }

func getBody(t *testing.T, admin *pgdesk.Admin, path string) string {
	t.Helper()
	rec := do(admin, httptest.NewRequest("GET", path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200", path, rec.Code)
	}
	return rec.Body.String()
}

func TestIntegrationTitle(t *testing.T) {
	admin, _ := setupWith(t, pgdesk.WithTitle("Ops Console"), pgdesk.WithResource("it_users", nil))

	body := getBody(t, admin, "/admin/")
	if !strings.Contains(body, `<span class="pg-brand-name">Ops Console</span>`) {
		t.Errorf("header does not show the title:\n%s", body)
	}
}

func TestIntegrationPageSize(t *testing.T) {
	admin, _ := setupWith(t, pgdesk.WithResource("it_users", func(r *pgdesk.Resource) { r.PageSize(1) }))

	body := getBody(t, admin, "/admin/it_users")
	if n := strings.Count(body, "data-pg-row-href="); n != 1 {
		t.Errorf("rows on page 1 = %d, want 1", n)
	}
	if !strings.Contains(body, "page=2") {
		t.Error("no link to page 2")
	}
}

func TestIntegrationRequired(t *testing.T) {
	const marker = `<label for="f_full_name">Full Name<span class="pg-req"`
	tests := []struct {
		name      string
		configure func(*pgdesk.Resource)
		want      bool
	}{
		{"nullable column is optional", nil, false},
		{"Required marks it required", func(r *pgdesk.Resource) { r.Required("full_name") }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admin, _ := setupWith(t, pgdesk.WithResource("it_users", tt.configure))
			if got := strings.Contains(getBody(t, admin, "/admin/it_users/new"), marker); got != tt.want {
				t.Errorf("full_name marked required = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestIntegrationConstraintMessage(t *testing.T) {
	const msg = "That email is already registered."
	admin, _ := setupWith(t, pgdesk.WithResource("it_users", func(r *pgdesk.Resource) {
		editableUsers(r)
		r.ConstraintMessage("it_users_email_key", msg)
	}))
	token, version := editToken(t, admin, "1")

	rec := postEdit(admin, "1", token, version, url.Values{"email": {"alan@example.com"}, "status": {"active"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, msg) || strings.Contains(body, "must be unique") {
		t.Errorf("want the configured message instead of the default:\n%s", body)
	}
}

func TestIntegrationVersionColumn(t *testing.T) {
	tests := []struct {
		name       string
		otherWrite string
		wantStatus int
		wantTitle  string
	}{
		{"a newer revision conflicts", "UPDATE it_docs SET revision = revision + 1", http.StatusConflict, "unpublished"},
		{"an unrelated column change does not", "UPDATE it_docs SET title = 'theirs'", http.StatusSeeOther, "mine"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admin, pool := setupSchema(t, docsSchema, pgdesk.WithResource("it_docs", func(r *pgdesk.Resource) {
				r.VersionColumn("revision")
				r.Readonly("revision")
			}))
			token, version := formTokens(t, admin, "/admin/it_docs/1/edit")
			if version != "1" {
				t.Fatalf("form version = %q, want the revision %q", version, "1")
			}
			if _, err := pool.Exec(context.Background(), tt.otherWrite); err != nil {
				t.Fatal(err)
			}

			form := url.Values{"title": {"mine"}, "_version": {version}}
			if rec := postForm(admin, "/admin/it_docs/1/edit", token, form); rec.Code != tt.wantStatus {
				t.Errorf("save after %q: status = %d, want %d", tt.otherWrite, rec.Code, tt.wantStatus)
			}
			var title string
			if err := pool.QueryRow(context.Background(), "SELECT title FROM it_docs WHERE id = 1").Scan(&title); err != nil {
				t.Fatal(err)
			}
			if title != tt.wantTitle {
				t.Errorf("title = %q, want %q", title, tt.wantTitle)
			}
		})
	}
}

func TestIntegrationIncludeViews(t *testing.T) {
	tests := []struct {
		name string
		opts []pgdesk.AutoRegisterOption
		want bool
	}{
		{"views are skipped by default", nil, false},
		{"IncludeViews registers the view", []pgdesk.AutoRegisterOption{pgdesk.IncludeViews("it_draft_docs")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admin, _ := setupSchema(t, docsSchema, pgdesk.WithAutoRegister(tt.opts...))
			if got := strings.Contains(getBody(t, admin, "/admin/"), `href="/admin/it_draft_docs"`); got != tt.want {
				t.Fatalf("view listed = %t, want %t", got, tt.want)
			}
			if !tt.want {
				return
			}
			if !strings.Contains(getBody(t, admin, "/admin/it_draft_docs"), "unpublished") {
				t.Error("view list does not show its row")
			}
			for _, path := range []string{"/admin/it_draft_docs/new", "/admin/it_draft_docs/1", "/admin/it_draft_docs/1/edit"} {
				if rec := do(admin, httptest.NewRequest("GET", path, nil)); rec.Code != http.StatusNotFound {
					t.Errorf("GET %s: status = %d, want 404", path, rec.Code)
				}
			}
		})
	}
}

// queryRecorder and auditRecorder need no locking: do serves each request on
// the test's goroutine.
type queryRecorder struct {
	ops  []string
	errs []error
}

func (m *queryRecorder) ObserveQuery(op string, _ time.Duration, err error) {
	m.ops = append(m.ops, op)
	if err != nil {
		m.errs = append(m.errs, err)
	}
}

func (m *queryRecorder) take() (ops []string, errs []error) {
	ops, errs = m.ops, m.errs
	m.ops, m.errs = nil, nil
	return ops, errs
}

func TestIntegrationMetrics(t *testing.T) {
	m := new(queryRecorder)
	admin, _ := setupWith(t,
		pgdesk.WithMetrics(m),
		pgdesk.WithResource("it_users", func(r *pgdesk.Resource) { r.SearchFields("email") }),
		pgdesk.WithResource("it_orders", nil),
	)

	tests := []struct {
		path string
		want []string
	}{
		{"/admin/it_users", []string{"list"}},
		{"/admin/it_users/1", []string{"detail"}},
		{"/admin/it_users/export.csv", []string{"export"}},
		{"/admin/it_orders", []string{"list", "fk_labels"}},
		{"/admin/it_users/options.json?q=ada", []string{"fk_options"}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			getBody(t, admin, tt.path)
			ops, errs := m.take()
			if !slices.Equal(ops, tt.want) {
				t.Errorf("observed %q, want %q", ops, tt.want)
			}
			for _, err := range errs {
				t.Errorf("observed error: %v", err)
			}
		})
	}
}

type auditRecorder struct {
	events []pgdesk.AuditEvent
	err    error
}

func (a *auditRecorder) LogAudit(_ context.Context, e pgdesk.AuditEvent) error {
	a.events = append(a.events, e)
	return a.err
}

func TestIntegrationAuditLogger(t *testing.T) {
	a := new(auditRecorder)
	admin, _ := setupWith(t, pgdesk.WithAuditLogger(a), pgdesk.WithResource("it_users", editableUsers))
	token, version := editToken(t, admin, "1")

	rec := postEdit(admin, "1", token, version, url.Values{"email": {"ada.new@example.com"}, "status": {"active"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, want 303", rec.Code)
	}

	if len(a.events) != 1 {
		t.Fatalf("got %d audit events, want 1", len(a.events))
	}
	e := a.events[0]
	if e.Action != pgdesk.AuditUpdate || e.Resource != "it_users" || e.Key != "1" || e.ActorID != "tester" {
		t.Errorf("event = {Action:%q Resource:%q Key:%q ActorID:%q}, want {update it_users 1 tester}",
			e.Action, e.Resource, e.Key, e.ActorID)
	}
	if got := e.After["email"]; got != "ada.new@example.com" {
		t.Errorf("After[email] = %v, want ada.new@example.com", got)
	}
}

func TestIntegrationAuditLoggerErrorKeepsChange(t *testing.T) {
	a := &auditRecorder{err: errors.New("audit sink down")}
	admin, pool := setupWith(t, pgdesk.WithAuditLogger(a), pgdesk.WithResource("it_users", editableUsers))
	token, version := editToken(t, admin, "1")

	rec := postEdit(admin, "1", token, version, url.Values{"email": {"ada.new@example.com"}, "status": {"active"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, want 303", rec.Code)
	}
	if len(a.events) != 1 {
		t.Errorf("got %d audit events, want 1", len(a.events))
	}
	var email string
	if err := pool.QueryRow(context.Background(), "SELECT email FROM it_users WHERE id = 1").Scan(&email); err != nil {
		t.Fatal(err)
	}
	if email != "ada.new@example.com" {
		t.Errorf("email = %q, want the committed change to stand", email)
	}
}
