package query

import (
	"strings"
	"testing"
)

func FuzzResolveColumn(f *testing.F) {
	tbl := testTable()
	valid := map[string]bool{}
	for _, c := range tbl.Columns() {
		valid[c.Name] = true
	}

	seeds := []string{

		"id", "email", "status", "prefs", "created_at", "external_id",

		"id; drop table users", `"; drop`, "email OR 1=1", "prefs->>'x'",
		"", "*", "count(*)", "EMAIL", " email", "email ", "\u0442\u0435\u0441\u0442", "e\u2019", "id::text",
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

		if !valid[c.Name] {
			t.Fatalf("resolver invented column %q from input %q", c.Name, name)
		}
		if c.Name != name {
			t.Fatalf("resolver accepted %q but returned column %q", name, c.Name)
		}

		if strings.ContainsAny(c.Name, `";`) {
			t.Fatalf("catalog column name contains SQL metacharacters: %q", c.Name)
		}
	})
}
