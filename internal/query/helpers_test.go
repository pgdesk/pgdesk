package query

import "github.com/pgdesk/pgdesk/internal/introspect"

func testTable() *introspect.Table {
	cols := []*introspect.Column{
		{Name: "id", Position: 1, DataType: "int8", Category: introspect.CatNumeric},
		{Name: "email", Position: 2, DataType: "text", Category: introspect.CatText},
		{Name: "full_name", Position: 3, DataType: "text", Category: introspect.CatText, Nullable: true},
		{Name: "status", Position: 4, DataType: "user_status", Category: introspect.CatEnum, EnumLabels: []string{"active", "suspended", "pending"}},
		{Name: "is_admin", Position: 5, DataType: "bool", Category: introspect.CatBool},
		{Name: "created_at", Position: 6, DataType: "timestamptz", Category: introspect.CatTimestamp},
		{Name: "external_id", Position: 7, DataType: "uuid", Category: introspect.CatUUID},
		{Name: "prefs", Position: 8, DataType: "jsonb", Category: introspect.CatJSON, Nullable: true},
	}
	pk := []*introspect.Column{cols[0]}
	return introspect.NewTable("public", "users", false, true, "", cols, pk, nil)
}

func col(t *introspect.Table, name string) *introspect.Column {
	c, ok := t.Column(name)
	if !ok {
		panic("test column not found: " + name)
	}
	return c
}

func twoColTable() *introspect.Table {
	cols := []*introspect.Column{
		{Name: "a", Position: 1, DataType: "int8", Category: introspect.CatNumeric},
		{Name: "b", Position: 2, DataType: "text", Category: introspect.CatText},
	}
	return introspect.NewTable("public", "pairs", false, true, "", cols, cols, nil)
}
