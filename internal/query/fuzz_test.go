package query

import (
	"strings"
	"testing"
)

// FuzzResolveColumn is the D3/O7 fuzz gate. For arbitrary request input, the
// resolver must either return a real catalog column (whose Name equals the input
// and is one of the table's columns) or reject it. It must never invent an
// identifier or panic. CI runs this target as a smoke check.
func FuzzResolveColumn(f *testing.F) {
	tbl := testTable()
	valid := map[string]bool{}
	for _, c := range tbl.Columns() {
		valid[c.Name] = true
	}

	seeds := []string{
		// Real columns (must resolve).
		"id", "email", "status", "prefs", "created_at", "external_id",
		// Injection / evasion payloads (must all be rejected, never emitted).
		"id; drop table users", `"; drop`, "email OR 1=1", "prefs->>'x'",
		"", "*", "count(*)", "EMAIL", " email", "email ", "тест", "e’", "id::text",
		`email"`, `email";`, "email--", "email/*c*/", "email\x00", "email\n",
		"1=1", "email,id", "(select 1)", "email\tstatus", "email` `",
		"pg_sleep(10)", "email::int", "0x41", "\\x41", "email|id",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, name string) {
		c, err := ResolveColumn(tbl, name)
		if err != nil {
			if c != nil {
				t.Fatalf("rejected %q but returned non-nil column", name)
			}
			return
		}
		// Accepted: it MUST be a genuine catalog column matching the input.
		if !valid[c.Name] {
			t.Fatalf("resolver invented column %q from input %q", c.Name, name)
		}
		if c.Name != name {
			t.Fatalf("resolver accepted %q but returned column %q", name, c.Name)
		}
		// And it must never carry SQL metacharacters that could have slipped
		// through, since it came from the catalog.
		if strings.ContainsAny(c.Name, `";`) {
			t.Fatalf("catalog column name contains SQL metacharacters: %q", c.Name)
		}
	})
}
