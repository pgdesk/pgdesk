package pgdesk

import (
	"fmt"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

type autoRegisterConfig struct {
	excludeTables map[string]bool
	includeViews  map[string]bool
}

// AutoRegisterOption configures WithAutoRegister.
type AutoRegisterOption func(*autoRegisterConfig)

// WithAutoRegister exposes every table in the configured schemas that has a primary key
// and a URL-safe name. Tables added with WithResource keep their configuration.
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

// ExcludeTables keeps the named tables out of auto-registration.
func ExcludeTables(names ...string) AutoRegisterOption {
	return func(ar *autoRegisterConfig) {
		for _, n := range names {
			ar.excludeTables[n] = true
		}
	}
}

// IncludeViews auto-registers the named views, which are skipped by default.
func IncludeViews(names ...string) AutoRegisterOption {
	return func(ar *autoRegisterConfig) {
		for _, n := range names {
			ar.includeViews[n] = true
		}
	}
}

func (a *Admin) autoRegister(cat *introspect.Catalog, add func(string, *Resource), existing map[string]*Resource) error {
	ar := a.cfg.autoRegister

	explicit := make(map[string]bool, len(existing))
	for name := range existing {
		explicit[name] = true
	}

	seen := map[string]*introspect.Table{}
	for _, tbl := range cat.Tables() {
		name := tbl.Name
		if explicit[name] {
			continue
		}
		if ar.excludeTables[name] {
			continue
		}
		if tbl.IsView && !ar.includeViews[name] {
			continue
		}
		if !tbl.HasKey() {
			continue
		}
		if !isURLSafe(name) {
			continue
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
