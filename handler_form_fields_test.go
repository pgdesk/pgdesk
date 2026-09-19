package pgdesk

import (
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

// formFieldsFor builds the form fields a resource would render for cols.
func formFieldsFor(t *testing.T, cols []*introspect.Column, row map[string]any) map[string]formField {
	t.Helper()
	a := testAdmin(t)
	res := &Resource{name: "orders", fields: map[string]*fieldConfig{}}
	out := map[string]formField{}
	for _, f := range a.buildFormFields(res, cols, row, nil) {
		out[f.Name] = f
	}
	return out
}

// PostgreSQL rejects any write but DEFAULT to a GENERATED ALWAYS AS IDENTITY
// column, and updatableColumns/editableColumns already drop it from every write.
// The form must agree: rendering it as an editable input invites the operator to
// type a value that is then silently discarded.
func TestBuildFormFieldsIdentityAlwaysIsReadonly(t *testing.T) {
	id := &introspect.Column{
		Name: "id", Position: 1, DataType: "int8",
		Category: introspect.CatNumeric, IsIdentityAlways: true,
	}
	name := &introspect.Column{
		Name: "name", Position: 2, DataType: "text",
		Category: introspect.CatText, Nullable: true,
	}
	fields := formFieldsFor(t, []*introspect.Column{id, name}, map[string]any{"id": int64(7), "name": "x"})

	if !fields["id"].Readonly {
		t.Error("a GENERATED ALWAYS AS IDENTITY column must render readonly")
	}
	// A column the operator cannot write must not be demanded of them either.
	if fields["id"].Required {
		t.Error("a GENERATED ALWAYS AS IDENTITY column must not be marked required")
	}
	// An ordinary column is unaffected.
	if fields["name"].Readonly {
		t.Error("an ordinary nullable column must stay editable")
	}
}

// GENERATED ALWAYS AS (expr) STORED columns were already readonly; lock that in
// alongside the identity fix so neither regresses.
func TestBuildFormFieldsGeneratedIsReadonly(t *testing.T) {
	gen := &introspect.Column{
		Name: "total", Position: 1, DataType: "numeric",
		Category: introspect.CatNumeric, IsGenerated: true,
	}
	fields := formFieldsFor(t, []*introspect.Column{gen}, map[string]any{"total": int64(3)})
	if !fields["total"].Readonly {
		t.Error("a generated column must render readonly")
	}
	if fields["total"].Required {
		t.Error("a generated column must not be marked required")
	}
}

// A NOT NULL column with no default and no generation is the one case the form
// should mark required -- proving the fix narrows the rule rather than gutting it.
func TestBuildFormFieldsPlainNotNullIsRequired(t *testing.T) {
	email := &introspect.Column{
		Name: "email", Position: 1, DataType: "text", Category: introspect.CatText,
	}
	fields := formFieldsFor(t, []*introspect.Column{email}, map[string]any{})
	if !fields["email"].Required {
		t.Error("a NOT NULL column with no default must be marked required")
	}
	if fields["email"].Readonly {
		t.Error("a NOT NULL column with no default must stay editable")
	}
}
