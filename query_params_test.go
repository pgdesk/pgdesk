package pgdesk

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

func filterTestResource() *Resource {
	cols := []*introspect.Column{
		{Name: "id", Position: 1, DataType: "int8", Category: introspect.CatNumeric},
		{Name: "email", Position: 2, DataType: "text", Category: introspect.CatText},
		{Name: "status", Position: 3, DataType: "st", Category: introspect.CatEnum, EnumLabels: []string{"active", "pending"}},
		{Name: "created_at", Position: 4, DataType: "timestamptz", Category: introspect.CatTimestamp},
	}
	pk := []*introspect.Column{cols[0]}
	tbl := introspect.NewTable("public", "users", false, true, "", cols, pk, nil)
	r := newResource("users", tbl, 50)
	r.ListDisplay("id", "email", "status", "created_at")
	r.SearchFields("email")
	r.Filters("status", "created_at")
	if r.err != nil {
		panic(r.err)
	}
	return r
}

func parse(t *testing.T, res *Resource, rawQuery string) (*listRequest, error) {
	t.Helper()
	a := &Admin{cfg: defaultConfig()}
	req := httptest.NewRequest("GET", "/admin/users", nil)
	req.URL.RawQuery = rawQuery // set directly so raw (unencoded) values don't panic NewRequest
	return a.parseListRequest(req, res)
}

func TestParseListValidFilters(t *testing.T) {
	res := filterTestResource()
	lr, err := parse(t, res, "f_status=active&f_created_at__gt=2024-01-01&q=ada&sort=-created_at&page=2&page_size=25")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(lr.filters) != 2 {
		t.Fatalf("expected 2 filters, got %d", len(lr.filters))
	}
	if lr.search == nil || lr.search.Term != "%ada%" {
		t.Fatalf("search not parsed: %+v", lr.search)
	}
	if lr.sortCol == nil || lr.sortCol.Name != "created_at" || !lr.sortDesc {
		t.Fatalf("sort not parsed: %+v desc=%v", lr.sortCol, lr.sortDesc)
	}
	if lr.page != 2 || lr.pageSize != 25 {
		t.Fatalf("pagination: page=%d size=%d", lr.page, lr.pageSize)
	}
}

// TestParseListFailsClosed is the D3 assertion for list params: anything not
// resolvable/permitted is rejected, never passed through.
func TestParseListFailsClosed(t *testing.T) {
	res := filterTestResource()
	cases := map[string]string{
		"unknown filter column":     "f_bogus=1",
		"non-filterable column":     "f_email__ilike=x", // email is searchable, not filterable
		"operator not for category": "f_status__lt=active",
		"bad value type":            "f_created_at__gt=not-a-date",
		"enum value not a label":    "f_status=deleted",
		"sort column not displayed": "sort=password",
		"injection in filter":       "f_status=active' OR 1=1--", // not a valid enum label -> rejected
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(t, res, q); !errors.Is(err, errBadRequest) {
				t.Fatalf("%s: expected errBadRequest, got %v", name, err)
			}
		})
	}
}

// TestEscapeLike proves issue #15a/#13c is fixed: LIKE metacharacters in a
// search/filter term are escaped (backslash first) so they match literally and
// a trailing backslash can't raise Postgres 22025.
func TestEscapeLike(t *testing.T) {
	cases := map[string]string{
		"50%":      `50\%`,
		"a_b":      `a\_b`,
		`back\end`: `back\\end`,
		`trail\`:   `trail\\`, // trailing backslash escaped -> no 22025
		"100%_off": `100\%\_off`,
		"plain":    "plain",
	}
	for in, want := range cases {
		if got := escapeLike(in); got != want {
			t.Errorf("escapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSearchTermEscaped proves the escape is applied where the term is wrapped
// with %...% for ILIKE search.
func TestSearchTermEscaped(t *testing.T) {
	res := filterTestResource()
	lr, err := parse(t, res, "q=50%25") // %25 decodes to a literal '%'
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if lr.search == nil || lr.search.Term != `%50\%%` {
		t.Fatalf("search term = %+v, want wrapped-and-escaped %q", lr.search, `%50\%%`)
	}
}

// TestILikeFilterEscaped proves the escape is applied on the text ilike filter
// path, including a trailing backslash that would otherwise error in Postgres.
func TestILikeFilterEscaped(t *testing.T) {
	res := filterTestResource()
	res.Filters("email")
	if res.err != nil {
		t.Fatalf("Filters: %v", res.err)
	}
	lr, err := parse(t, res, `f_email__ilike=a_b%5C`) // %5C decodes to a trailing backslash
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(lr.filters) != 1 {
		t.Fatalf("want 1 filter, got %d", len(lr.filters))
	}
	term, _ := lr.filters[0].Values[0].(string)
	if term != `%a\_b\\%` {
		t.Fatalf("ilike term = %q, want %q", term, `%a\_b\\%`)
	}
}

func TestParseListPageSizeClamped(t *testing.T) {
	res := filterTestResource()
	lr, err := parse(t, res, "page_size=100000")
	if err != nil {
		t.Fatal(err)
	}
	if lr.pageSize != defaultConfig().maxPageSize {
		t.Fatalf("page_size not clamped: %d", lr.pageSize)
	}
}

func TestParseListEmptyFilterIgnored(t *testing.T) {
	res := filterTestResource()
	lr, err := parse(t, res, "f_status=")
	if err != nil {
		t.Fatal(err)
	}
	if len(lr.filters) != 0 {
		t.Fatalf("empty filter should be ignored, got %d", len(lr.filters))
	}
}
