//go:build integration

package pgdesk_test

import (
	"bytes"
	"encoding/csv"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk"
)

func TestIntegrationBulkActionRespectsMaxBulk(t *testing.T) {
	tests := []struct {
		name        string
		keys        []string
		wantCode    int
		wantChanged bool
		wantBody    string
	}{
		{"at the limit", []string{"2", leavingID}, http.StatusSeeOther, true, ""},
		{"over the limit", []string{"1", "2", leavingID}, http.StatusBadRequest, false, "Too many rows selected (max 2)."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admin, pool := usersAdmin(t, pgdesk.WithMaxBulk(2))
			token, _ := editToken(t, admin, "1")
			before := usersSnapshot(t, pool)

			rec := postForm(admin, "/admin/it_users/action", token, url.Values{"_action": {"activate"}, "key": tt.keys})

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tt.wantCode, rec.Body)
			}
			if changed := usersSnapshot(t, pool) != before; changed != tt.wantChanged {
				t.Errorf("rows changed = %t, want %t", changed, tt.wantChanged)
			}
			if body := rec.Body.String(); !strings.Contains(body, tt.wantBody) {
				t.Errorf("response does not contain %q\n%s", tt.wantBody, body)
			}
		})
	}
}

func TestIntegrationExportRespectsMaxExportRows(t *testing.T) {
	// usersAdmin has three users.
	tests := []struct {
		name     string
		max      int
		wantRows int
		wantWarn bool
	}{
		{"under the limit", 4, 3, false},
		{"at the limit", 3, 3, false},
		{"over the limit", 2, 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			admin, _ := usersAdmin(t,
				pgdesk.WithMaxExportRows(tt.max),
				pgdesk.WithLogger(slog.New(slog.NewTextHandler(&logs, nil))),
			)

			rec := do(admin, httptest.NewRequest("GET", "/admin/it_users/export.csv", nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			records, err := csv.NewReader(rec.Body).ReadAll()
			if err != nil {
				t.Fatalf("parse CSV: %v", err)
			}
			if got := len(records) - 1; got != tt.wantRows {
				t.Errorf("exported %d rows, want %d", got, tt.wantRows)
			}
			if warned := strings.Contains(logs.String(), "export truncated"); warned != tt.wantWarn {
				t.Errorf("truncation logged = %t, want %t\n%s", warned, tt.wantWarn, logs.String())
			}
		})
	}
}
