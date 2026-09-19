package pgdesk

import (
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/pgdesk/pgdesk/internal/render"
)

var update = flag.Bool("update", false, "update golden files")

var (
	reNonce = regexp.MustCompile(`nonce="[^"]*"`)
	reCSRF  = regexp.MustCompile(`value="[^"]*\.[^"]*"`)
)

func normalizeHTML(s string) string {
	s = reNonce.ReplaceAllString(s, `nonce="NONCE"`)
	s = reCSRF.ReplaceAllString(s, `value="CSRF"`)
	return s
}

func TestGoldenPages(t *testing.T) {
	a := testAdmin(t)
	base := baseView{Title: "Users", SiteTitle: "Ops", BasePath: "/admin"}
	xss := `<script>alert('x')</script>`

	cases := map[string]struct {
		page string
		data any
	}{
		"list": {"list", listView{
			Base: base, Resource: resourceMeta{Name: "users", Label: "User", LabelPlural: "Users"},
			Headers:       []sortHeader{{Label: "Email", URL: "/admin/users?sort=email"}},
			Rows:          []rowView{{Cells: []cellView{{Value: xss}, {Label: "ref " + xss, Link: "/admin/orgs/1"}}, Key: "1"}},
			InlineFilters: []filterField{{Label: "Status", Kind: "select", ParamKey: "f_status", Options: []string{"", "active"}}},

			ActiveChips: []filterChip{{Label: "Status", Value: xss, RemoveURL: "/admin/users"}},
			ActiveCount: 1,
			Actions:     []actionMeta{{Name: "suspend", Label: "Suspend " + xss, Confirm: "Sure?"}},
			HasActions:  true, HasFilters: true, HasDetail: true, CanCreate: true,
			ExportURL: "/admin/users/export.csv", Page: 1,
		}},
		"detail": {"detail", detailView{
			Base: base, Resource: resourceMeta{Name: "users", Label: "User"},
			Key: "1", CanEdit: true, CanDelete: true,
			Fields: []detailField{{Label: "Email", Value: xss}},
		}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rf := render.RequestFuncs{CSRFToken: "tok.tok", Nonce: "abc"}
			if err := a.renderer.RenderPage(rec, 200, tc.page, rf, tc.data); err != nil {
				t.Fatal(err)
			}
			got := normalizeHTML(rec.Body.String())
			goldenPath := filepath.Join("testdata", "golden", name+".html")
			if *update {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("golden mismatch for %s; run: go test -run TestGolden -update", name)
			}
		})
	}
}
