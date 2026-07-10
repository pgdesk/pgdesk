package pgdesk

import (
	"fmt"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

// autoRegisterConfig holds the opt-in auto-registration policy (D2). It is nil
// unless WithAutoRegister is used.
type autoRegisterConfig struct {
	excludeTables map[string]bool
	includeViews  map[string]bool
}

// AutoRegisterOption configures WithAutoRegister.
type AutoRegisterOption func(*autoRegisterConfig)

// WithAutoRegister opts into convenience auto-registration (D2). It exposes every
// introspected table that has a primary key, in the configured schemas, EXCEPT:
//   - tables in the ExcludeTables list,
//   - tables without a primary key (they cannot be safely keyed),
//   - views (unless named via IncludeViews).
//
// Explicitly-registered resources (Admin.Resource) always win over auto-registered
// ones and are never overridden. The full exposed set is logged at startup so an
// operator can immediately spot a leaked PII or system table. Auto-registration
// is still fail-closed: it never publishes an unkeyed table, and if it would
// expose a writable table while no CSRF key is configured, construction fails.
func WithAutoRegister(opts ...AutoRegisterOption) Option {
	return func(c *config) {
		ar := &autoRegisterConfig{
			excludeTables: map[string]bool{},
			includeViews:  map[string]bool{},
		}
		for _, o := range opts {
			o(ar)
		}
		c.autoRegister = ar
	}
}

// ExcludeTables names tables that auto-registration must never expose — junction
// tables, audit logs, system/migration tables, or anything holding PII you do not
// want surfaced (D2).
func ExcludeTables(names ...string) AutoRegisterOption {
	return func(ar *autoRegisterConfig) {
		for _, n := range names {
			ar.excludeTables[n] = true
		}
	}
}

// IncludeViews names views that auto-registration should expose. Views are
// skipped by default because they are often non-updatable or derived; naming one
// opts it in (as a list-only or, if updatable, editable resource per D6).
func IncludeViews(names ...string) AutoRegisterOption {
	return func(ar *autoRegisterConfig) {
		for _, n := range names {
			ar.includeViews[n] = true
		}
	}
}

// autoRegister appends default resources for eligible tables to the state being
// built. existing holds already-added (explicit) resources, which win. add
// registers a resource in discovery order.
//
// Each resource is built from the table the loop discovered, never by re-resolving
// its bare name: two schemas may hold a table of the same name, and a resource is
// keyed by the bare name alone. Such a collision fails the build rather than
// exposing whichever table sorts first (D2).
func (a *Admin) autoRegister(cat *introspect.Catalog, add func(string, *Resource), existing map[string]*Resource) error {
	ar := a.cfg.autoRegister

	// existing is the live resource map that add writes into, so snapshot the
	// explicit names first. Testing against it directly would let this loop's own
	// first "users" mask a second "users" from another schema as if it had been
	// registered by hand.
	explicit := make(map[string]bool, len(existing))
	for name := range existing {
		explicit[name] = true
	}

	seen := map[string]*introspect.Table{}
	for _, tbl := range cat.Tables() {
		name := tbl.Name
		if explicit[name] {
			continue // explicit registration wins
		}
		if ar.excludeTables[name] {
			continue
		}
		if tbl.IsView && !ar.includeViews[name] {
			continue
		}
		if !tbl.HasKey() {
			continue // never publish an unkeyed table
		}
		if !isURLSafe(name) {
			continue // the name is the route; it cannot address this table
		}
		if prev, dup := seen[name]; dup {
			return fmt.Errorf("auto-register %q: %w: %q and %q",
				name, ErrAmbiguousTable, prev.Schema, tbl.Schema)
		}
		seen[name] = tbl

		r, err := a.buildResourceFrom(tbl, name, nil)
		if err != nil {
			return fmt.Errorf("auto-register %q: %w", name, err)
		}
		add(name, r)
	}
	return nil
}
