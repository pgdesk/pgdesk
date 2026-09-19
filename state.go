package pgdesk

import "github.com/pgdesk/pgdesk/internal/introspect"

type adminState struct {
	catalog   *introspect.Catalog
	resources map[string]*Resource
	order     []string
}

func (s *adminState) resource(name string) (*Resource, bool) {
	r, ok := s.resources[name]
	return r, ok
}

type resourceReg struct {
	name string
	fn   func(*Resource)
}
