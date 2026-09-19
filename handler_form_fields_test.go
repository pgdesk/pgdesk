package pgdesk

import (
	"net/http/httptest"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

func formFieldsFor(t *testing.T, cols []*introspect.Column, row map[string]any) map[string]formField {
	t.Helper()
	a := testAdmin(t)
	res := &Resource{name: "orders", fields: map[string]*fieldConfig{}}
	req := httptest.NewRequest("GET", "/admin/orders/1/edit", nil)
	out := map[string]formField{}
	for _, f := range a.buildFormFields(req, res, cols, row, nil) {
		out[f.Name] = f
	}
	return out
}

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

	if fields["id"].Required {
		t.Error("a GENERATED ALWAYS AS IDENTITY column must not be marked required")
	}

	if fields["name"].Readonly {
		t.Error("an ordinary nullable column must stay editable")
	}
}

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
