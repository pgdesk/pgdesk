package pgdesk

import (
	"errors"
	"strings"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

// peopleTable is a keyed table with a numeric id and two text columns, used to
// exercise the LabelColumn override and the first-text-column heuristic.
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

// LabelColumn sets labelCol to the resolved column on success, and records an
// ErrUnknownColumn configuration error for an unknown column without touching
// labelCol.
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

// labelColumn prefers a resource's explicit LabelColumn over the first-text-column
// heuristic, and falls back to the heuristic when none is set.
func TestLabelColumnFunctionPrefersExplicitOverride(t *testing.T) {
	r := peopleResource()
	pk := r.table.PrimaryKey[0]

	// Unset: falls back to the first non-hidden text column (full_name, ordinal
	// before email).
	if got := labelColumn(r, pk); got.Name != "full_name" {
		t.Fatalf("labelColumn heuristic = %q, want full_name", got.Name)
	}

	// Explicit override wins even though it is not the first text column.
	r.LabelColumn("email")
	if r.err != nil {
		t.Fatalf("LabelColumn(email): unexpected error %v", r.err)
	}
	if got := labelColumn(r, pk); got.Name != "email" {
		t.Fatalf("labelColumn override = %q, want email", got.Name)
	}
}

// A resolve failure names the setter that referenced the bad column, so a
// misconfiguration is traceable back to its call site, while still satisfying
// errors.Is(err, ErrUnknownColumn) for existing callers.
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
