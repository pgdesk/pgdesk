package pgdesk

import (
	"fmt"

	"github.com/pgdesk/pgdesk/internal/introspect"
)

type autoRegisterConfig struct {
	excludeTables map[string]bool
	includeViews  map[string]bool
}

type AutoRegisterOption func(*autoRegisterConfig)

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

func ExcludeTables(names ...string) AutoRegisterOption {
	return func(ar *autoRegisterConfig) {
		for _, n := range names {
			ar.excludeTables[n] = true
		}
	}
}

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
