package pgdesk

import (
	"errors"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

func col(name string) *introspect.Column {
	return &introspect.Column{Name: name, Position: 1, DataType: "int8", Category: introspect.CatNumeric}
}

func keyedTable(schema, name string, fks ...*introspect.ForeignKey) *introspect.Table {
	id := col("id")
	return introspect.NewTable(schema, name, false, true, "",
		[]*introspect.Column{id}, []*introspect.Column{id}, fks)
}

func multiSchemaCatalog() *introspect.Catalog {
	return introspect.NewCatalog([]string{"public", "billing"}, []*introspect.Table{
		keyedTable("public", "users"),
		keyedTable("billing", "users"),
		keyedTable("billing", "invoices"),
	})
}

func autoCfg(exclude ...string) *autoRegisterConfig {
	ar := &autoRegisterConfig{excludeTables: map[string]bool{}, includeViews: map[string]bool{}}
	for _, n := range exclude {
		ar.excludeTables[n] = true
	}
	return ar
}

func TestResolveTableRejectsAmbiguousName(t *testing.T) {
	cat := multiSchemaCatalog()
	both := []string{"public", "billing"}

	if _, err := resolveTable(cat, both, "users"); !errors.Is(err, ErrAmbiguousTable) {
		t.Fatalf("resolveTable(users) error = %v, want ErrAmbiguousTable", err)
	}

	tbl, err := resolveTable(cat, both, "invoices")
	if err != nil {
		t.Fatalf("resolveTable(invoices): %v", err)
	}
	if tbl.Schema != "billing" {
		t.Errorf("invoices resolved to schema %q, want billing", tbl.Schema)
	}

	tbl, err = resolveTable(cat, []string{"public"}, "users")
	if err != nil {
		t.Fatalf("resolveTable(users) with one schema: %v", err)
	}
	if tbl.Schema != "public" {
		t.Errorf("users resolved to schema %q, want public", tbl.Schema)
	}

	if _, err := resolveTable(cat, both, "nope"); !errors.Is(err, ErrUnknownTable) {
		t.Errorf("unknown name error = %v, want ErrUnknownTable", err)
	}
}

func TestBuildStateRejectsAmbiguousResource(t *testing.T) {
	a := testAdmin(t, func(c *config) {
		c.schemas = []string{"public", "billing"}
		c.resources = []resourceReg{{name: "users"}}
	})
	if _, err := a.buildState(multiSchemaCatalog()); !errors.Is(err, ErrAmbiguousTable) {
		t.Fatalf("buildState error = %v, want ErrAmbiguousTable", err)
	}
}

func TestAutoRegisterRejectsAmbiguousName(t *testing.T) {
	a := testAdmin(t, func(c *config) {
		c.schemas = []string{"public", "billing"}
		c.autoRegister = autoCfg()
	})
	if _, err := a.buildState(multiSchemaCatalog()); !errors.Is(err, ErrAmbiguousTable) {
		t.Fatalf("buildState error = %v, want ErrAmbiguousTable", err)
	}
}

func TestAutoRegisterBindsDiscoveredTable(t *testing.T) {
	a := testAdmin(t, func(c *config) {
		c.schemas = []string{"public", "billing"}
		c.autoRegister = autoCfg("users")
	})
	st, err := a.buildState(multiSchemaCatalog())
	if err != nil {
		t.Fatalf("buildState: %v", err)
	}
	res, ok := st.resource("invoices")
	if !ok {
		t.Fatal("invoices was not auto-registered")
	}
	if res.table.Schema != "billing" || res.table.Name != "invoices" {
		t.Errorf("invoices bound to %s.%s, want billing.invoices", res.table.Schema, res.table.Name)
	}
}

func TestResourceNameMustBeURLSafe(t *testing.T) {
	cat := introspect.NewCatalog([]string{"public"}, []*introspect.Table{
		keyedTable("public", "user profiles"),
	})
	a := testAdmin(t, func(c *config) {
		c.resources = []resourceReg{{name: "user profiles"}}
	})
	if _, err := a.buildState(cat); !errors.Is(err, ErrUnsafeName) {
		t.Fatalf("buildState error = %v, want ErrUnsafeName", err)
	}
}

func TestAutoRegisterSkipsUnsafeNames(t *testing.T) {
	cat := introspect.NewCatalog([]string{"public"}, []*introspect.Table{
		keyedTable("public", "user profiles"),
		keyedTable("public", "users"),
	})
	a := testAdmin(t, func(c *config) { c.autoRegister = autoCfg() })
	st, err := a.buildState(cat)
	if err != nil {
		t.Fatalf("buildState: %v", err)
	}
	if _, ok := st.resource("user profiles"); ok {
		t.Error("auto-register exposed a name that is not URL-safe")
	}
	if _, ok := st.resource("users"); !ok {
		t.Error("auto-register skipped a valid table")
	}
}

func TestResolveRefUsesForeignKeySchema(t *testing.T) {
	acctID := col("id")
	pubAccts := keyedTable("public", "accounts")
	bilAccts := keyedTable("billing", "accounts")
	fk := &introspect.ForeignKey{
		Columns: []string{"account_id"}, RefSchema: "billing",
		RefTable: "accounts", RefColumns: []string{"id"},
	}
	orders := introspect.NewTable("public", "orders", false, true, "",
		[]*introspect.Column{acctID}, []*introspect.Column{acctID}, []*introspect.ForeignKey{fk})
	cat := introspect.NewCatalog([]string{"public", "billing"},
		[]*introspect.Table{pubAccts, bilAccts, orders})

	ref, ok := resolveRef(cat, fk)
	if !ok {
		t.Fatal("resolveRef failed")
	}
	if ref.Schema != "billing" {
		t.Fatalf("foreign key to billing.accounts resolved to %q.accounts", ref.Schema)
	}

	outside := &introspect.ForeignKey{RefSchema: "internal", RefTable: "accounts"}
	if _, ok := resolveRef(cat, outside); ok {
		t.Error("resolveRef found a table outside the catalog")
	}
}

func TestRefResourceRequiresMatchingTable(t *testing.T) {
	pubAccts := keyedTable("public", "accounts")
	bilAccts := keyedTable("billing", "accounts")
	st := &adminState{resources: map[string]*Resource{
		"accounts": {name: "accounts", table: pubAccts},
	}}

	if _, ok := refResource(st, pubAccts); !ok {
		t.Error("the registered resource's own table must match")
	}
	if _, ok := refResource(st, bilAccts); ok {
		t.Error("a same-named table in another schema must not match")
	}
	if _, ok := refResource(st, keyedTable("public", "ledger")); ok {
		t.Error("an unregistered table must not match")
	}
}

func TestResourceHasNoAuthorizeMethod(t *testing.T) {
	var r any = &Resource{}
	if _, ok := r.(interface{ Authorize(Authorizer) }); ok {
		t.Fatal("Resource.Authorize must not exist; authorization is admin-wide")
	}
}
