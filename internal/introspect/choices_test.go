package introspect

import (
	"slices"
	"testing"
)

func TestParseCheckChoices(t *testing.T) {
	cases := []struct {
		def  string
		want []string
	}{
		{`CHECK ((scope = ANY (ARRAY['all'::text, 'assigned'::text])))`, []string{"all", "assigned"}},
		{`CHECK (((kind)::text = ANY ((ARRAY['a'::character varying, 'b'::character varying])::text[])))`, []string{"a", "b"}},
		{`CHECK ((status = ANY (ARRAY['it''s'::text, 'x y'::text])))`, []string{"it's", "x y"}},
		{`CHECK ((role = 'owner'::text))`, nil},
		{`CHECK ((length(note) < 100))`, nil},
		{`CHECK ((status <> ALL (ARRAY['a'::text, 'b'::text])))`, nil},
		{`CHECK ((n = ANY (ARRAY[1, 2])))`, nil},
		{`CHECK (((s = ANY (ARRAY['a'::text])) AND (t > 0)))`, nil},
	}
	for _, c := range cases {
		if got := parseCheckChoices(c.def); !slices.Equal(got, c.want) {
			t.Errorf("parseCheckChoices(%q) = %q, want %q", c.def, got, c.want)
		}
	}
}
