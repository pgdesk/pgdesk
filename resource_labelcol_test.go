package pgdesk

import (
	"errors"
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

func peopleTable() *introspect.Table {
	id := col("id")
	fullName := textCol("full_name")
	email := textCol("email")
	return introspect.NewTable("public", "people", false, true, "",
		[]*introspect.Column{id, fullName, email}, []*introspect.Column{id}, nil)
}

func peopleResource() *Resource {
	return newResource("people", peopleTable(), 50)
}

func TestLabelColumnSetsOrErrors(t *testing.T) {
	r := peopleResource()
	r.LabelColumn("full_name")
	if r.err != nil {
		t.Fatalf("LabelColumn(full_name): unexpected error %v", r.err)
	}
	if r.labelCol == nil || r.labelCol.Name != "full_name" {
		t.Fatalf("labelCol = %+v, want full_name", r.labelCol)
	}

	r2 := peopleResource()
	r2.LabelColumn("nope")
	if r2.labelCol != nil {
		t.Errorf("labelCol should remain unset after a failed LabelColumn call, got %+v", r2.labelCol)
	}
	if !errors.Is(r2.err, ErrUnknownColumn) {
		t.Fatalf("LabelColumn(nope) error = %v, want ErrUnknownColumn", r2.err)
	}
}

func TestLabelColumnFunctionPrefersExplicitOverride(t *testing.T) {
	r := peopleResource()
	pk := r.table.PrimaryKey[0]

	if got := labelColumn(r, pk); got.Name != "full_name" {
		t.Fatalf("labelColumn heuristic = %q, want full_name", got.Name)
	}

	r.LabelColumn("email")
	if r.err != nil {
		t.Fatalf("LabelColumn(email): unexpected error %v", r.err)
	}
	if got := labelColumn(r, pk); got.Name != "email" {
		t.Fatalf("labelColumn override = %q, want email", got.Name)
	}
}

func TestResolveErrorNamesCallingSetter(t *testing.T) {
	r := peopleResource()
	r.ListDisplay("bad_column")

	if r.err == nil {
		t.Fatal("expected a configuration error for an unknown column")
	}
	if !errors.Is(r.err, ErrUnknownColumn) {
		t.Fatalf("error = %v, want errors.Is match against ErrUnknownColumn", r.err)
	}
	if !strings.Contains(r.err.Error(), "referenced by ListDisplay") {
		t.Fatalf("error %q does not name the calling setter", r.err.Error())
	}
}
