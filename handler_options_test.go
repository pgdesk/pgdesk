package pgdesk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

// fkState builds a catalog with orders.user_id -> users.id and registers both as
// resources, plus a request carrying that state. registerUsers controls whether
// the referenced table is a registered resource.
func fkState(t *testing.T, registerUsers bool, az Authorizer) (*Admin, *Resource, *adminState) {
	t.Helper()
	usersID := col("id")
	email := &introspect.Column{Name: "email", Position: 2, DataType: "text", Category: introspect.CatText}
	users := introspect.NewTable("public", "users", false, true, "",
		[]*introspect.Column{usersID, email}, []*introspect.Column{usersID}, nil)

	fk := &introspect.ForeignKey{
		Columns: []string{"user_id"}, RefSchema: "public",
		RefTable: "users", RefColumns: []string{"id"},
	}
	ordersID := col("id")
	userID := col("user_id")
	orders := introspect.NewTable("public", "orders", false, true, "",
		[]*introspect.Column{ordersID, userID}, []*introspect.Column{ordersID},
		[]*introspect.ForeignKey{fk})

	cat := introspect.NewCatalog([]string{"public"}, []*introspect.Table{users, orders})
	ordersRes := &Resource{name: "orders", table: orders, keyCols: []*introspect.Column{ordersID}, fields: map[string]*fieldConfig{}}
	st := &adminState{catalog: cat, resources: map[string]*Resource{"orders": ordersRes}, order: []string{"orders"}}
	if registerUsers {
		st.resources["users"] = &Resource{
			name: "users", table: users, keyCols: []*introspect.Column{usersID},
			fields: map[string]*fieldConfig{}, labelCol: email,
		}
		st.order = append(st.order, "users")
	}
	a := testAdmin(t, func(c *config) { c.authorizer = az })
	return a, ordersRes, st
}

// fkRequest carries the state snapshot AND a principal: authorization fails
// closed without one, so a request lacking a principal gets no picker.
func fkRequest(st *adminState) *http.Request {
	r := httptest.NewRequest("GET", "/admin/orders/1/edit", nil)
	ctx := withState(r.Context(), st)
	ctx = WithPrincipal(ctx, testPrincipal{id: "tester"})
	return r.WithContext(ctx)
}

func fkFormFields(t *testing.T, registerUsers bool, az Authorizer) map[string]formField {
	t.Helper()
	a, res, st := fkState(t, registerUsers, az)
	r := fkRequest(st)
	out := map[string]formField{}
	// A nil foreign key -- the create-form shape -- so these cases assert the
	// widget wiring without a database. The label lookup for an existing value is
	// covered by TestIntegrationFKFormRendersPicker against real PostgreSQL.
	for _, f := range a.buildFormFields(r, res, res.table.Columns(), map[string]any{"id": int64(1), "user_id": nil}, nil) {
		out[f.Name] = f
	}
	return out
}

// A foreign-key column whose referenced table is a registered resource becomes a
// picker: the template needs the widget and the referenced resource name to point
// the options endpoint at.
func TestBuildFormFieldsMarksForeignKeyPicker(t *testing.T) {
	fields := fkFormFields(t, true, AllowAll)

	if got := fields["user_id"].Widget; got != string(WidgetFK) {
		t.Errorf("user_id widget = %q, want %q", got, WidgetFK)
	}
	if got := fields["user_id"].Ref; got != "users" {
		t.Errorf("user_id ref resource = %q, want users", got)
	}
	// The plain key column is untouched.
	if got := fields["id"].Widget; got == string(WidgetFK) {
		t.Error("a non-FK column must not become a picker")
	}
}

// Nothing is exposed until it is named: an FK pointing at a table the host never
// registered gets no picker, because there is no resource whose authorizer and
// row scope could govern reading it.
func TestBuildFormFieldsSkipsPickerForUnregisteredTable(t *testing.T) {
	fields := fkFormFields(t, false, AllowAll)

	if got := fields["user_id"].Widget; got == string(WidgetFK) {
		t.Error("an FK to an unregistered table must not become a picker")
	}
	if got := fields["user_id"].Ref; got != "" {
		t.Errorf("user_id ref resource = %q, want empty", got)
	}
}

// The picker reads another table, so it needs CapView on the referenced resource.
// Without it the field stays a plain input: no picker, and no hint that the
// referenced rows exist.
func TestBuildFormFieldsSkipsPickerWithoutViewOnReference(t *testing.T) {
	denyUsers := AuthorizerFunc(func(ctx context.Context, attrs Attributes) (Decision, error) {
		if attrs.Resource == "users" {
			return Deny, nil
		}
		return Allow, nil
	})
	fields := fkFormFields(t, true, denyUsers)

	if got := fields["user_id"].Widget; got == string(WidgetFK) {
		t.Error("a picker was offered for a resource the principal may not view")
	}
	if got := fields["user_id"].Ref; got != "" {
		t.Errorf("user_id ref resource = %q, want empty", got)
	}
}

// A host that asked for a specific widget keeps it: the FK default must not
// silently override an explicit choice.
func TestBuildFormFieldsExplicitWidgetBeatsFKDefault(t *testing.T) {
	a, res, st := fkState(t, true, AllowAll)
	res.fields["user_id"] = &fieldConfig{widget: WidgetTextarea}
	r := fkRequest(st)

	for _, f := range a.buildFormFields(r, res, res.table.Columns(), map[string]any{"user_id": nil}, nil) {
		if f.Name == "user_id" && f.Widget != string(WidgetTextarea) {
			t.Errorf("user_id widget = %q, want the explicit %q", f.Widget, WidgetTextarea)
		}
	}
}
