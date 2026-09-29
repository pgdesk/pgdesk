//go:build integration

package pgdesk_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgdesk/pgdesk"
)

// tenantDDL puts every user in a tenant. Ada and Grace are in acme; Alan and
// Linus are in globex. Grace and Linus have no orders, so they can be deleted.
const tenantDDL = `
ALTER TABLE it_users ADD COLUMN tenant text NOT NULL DEFAULT 'acme';
UPDATE it_users SET tenant = 'globex' WHERE email = 'alan@example.com';
INSERT INTO it_users (email, tenant) VALUES
    ('grace@example.com', 'acme'),
    ('linus@example.com', 'globex');
`

// suspendRecorder is a bulk action that suspends users and counts its calls.
// The handler runs on the test's goroutine, so calls needs no lock.
type suspendRecorder struct{ calls int }

func (s *suspendRecorder) suspend(ctx context.Context, tx pgx.Tx, keys pgdesk.Keys) (string, error) {
	ids, err := keys.Int64s()
	if err != nil {
		return "", err
	}
	s.calls++
	_, err = tx.Exec(ctx, "UPDATE it_users SET status = 'suspended' WHERE id = ANY($1)", ids)
	return "", err
}

// acmeAdmin serves it_users to an operator scoped to the acme tenant.
func acmeAdmin(t *testing.T) (*pgdesk.Admin, *pgxpool.Pool, *suspendRecorder) {
	t.Helper()
	acmeOnly := pgdesk.ScopeOnly(func(context.Context, pgdesk.Attributes) ([]pgdesk.Constraint, error) {
		return []pgdesk.Constraint{pgdesk.Eq("tenant", "acme")}, nil
	})
	rec := new(suspendRecorder)
	admin, pool := setupSchema(t, tenantDDL,
		pgdesk.WithAuthorizer(pgdesk.DenyOverrides(pgdesk.AllowAll, acmeOnly)),
		pgdesk.WithResource("it_users", func(r *pgdesk.Resource) {
			r.Action("suspend", "Suspend", rec.suspend)
		}),
	)
	return admin, pool, rec
}

func TestIntegrationDeleteStaysInScope(t *testing.T) {
	tests := []struct {
		name        string
		email       string
		wantCode    int
		wantDeleted bool
	}{
		{"row in scope is deleted", "grace@example.com", http.StatusSeeOther, true},
		{"row out of scope is not found", "linus@example.com", http.StatusNotFound, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admin, pool, _ := acmeAdmin(t)
			token, _ := editToken(t, admin, "1")
			id := strconv.FormatInt(userID(t, pool, tt.email), 10)

			rec := postForm(admin, "/admin/it_users/"+id+"/delete", token, url.Values{})

			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if deleted := !userExists(t, pool, tt.email); deleted != tt.wantDeleted {
				t.Errorf("row deleted = %t, want %t", deleted, tt.wantDeleted)
			}
		})
	}
}

func TestIntegrationBulkActionStaysInScope(t *testing.T) {
	tests := []struct {
		name       string
		emails     []string
		wantCalls  int
		wantStatus map[string]string
		wantFlash  string
	}{
		{
			name:       "rows in scope are changed",
			emails:     []string{"ada@example.com", "grace@example.com"},
			wantCalls:  1,
			wantStatus: map[string]string{"ada@example.com": "suspended", "grace@example.com": "suspended"},
			wantFlash:  "Suspend applied to 2 row(s).",
		},
		{
			name:       "one row out of scope refuses the whole action",
			emails:     []string{"ada@example.com", "alan@example.com"},
			wantCalls:  0,
			wantStatus: map[string]string{"ada@example.com": "active", "alan@example.com": "pending"},
			wantFlash:  "The action could not be completed.",
		},
		{
			name:       "rows only out of scope are refused",
			emails:     []string{"alan@example.com"},
			wantCalls:  0,
			wantStatus: map[string]string{"alan@example.com": "pending"},
			wantFlash:  "The action could not be completed.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admin, pool, action := acmeAdmin(t)
			token, _ := editToken(t, admin, "1")
			form := url.Values{"_action": {"suspend"}}
			for _, email := range tt.emails {
				form.Add("key", strconv.FormatInt(userID(t, pool, email), 10))
			}

			body := followRedirect(t, admin, postForm(admin, "/admin/it_users/action", token, form))

			if action.calls != tt.wantCalls {
				t.Errorf("action ran %d times, want %d", action.calls, tt.wantCalls)
			}
			for email, want := range tt.wantStatus {
				if got := userStatus(t, pool, email); got != want {
					t.Errorf("%s status = %q, want %q", email, got, want)
				}
			}
			if !strings.Contains(body, tt.wantFlash) {
				t.Errorf("page after the action does not contain %q\n%s", tt.wantFlash, body)
			}
		})
	}
}
