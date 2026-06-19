package pgdesk

import "github.com/pgdesk/pgdesk/internal/introspect"

// adminState is an immutable, atomically-swapped bundle of the catalog and the
// resources built against it (D1). A request captures one *adminState at its top
// and uses it throughout, so a mid-request Reload cannot shift either the schema
// or the resource set underneath a handler. Reload builds a fresh adminState and
// swaps the pointer; on failure the previous state stays live (degrade to stale,
// never to broken).
type adminState struct {
	catalog   *introspect.Catalog
	resources map[string]*Resource
	order     []string // resource names in registration/discovery order
}

func (s *adminState) resource(name string) (*Resource, bool) {
	r, ok := s.resources[name]
	return r, ok
}

// resourceReg is a retained resource registration. Keeping the config closure
// lets Reload rebuild every resource against the new catalog rather than leaving
// resources bound to a stale one.
type resourceReg struct {
	name string
	fn   func(*Resource)
}
