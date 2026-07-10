package pgdesk

import "errors"

// Sentinel errors returned by construction and configuration. Request-path
// errors are handled internally and never leak to the browser (F5); these are
// for the host wiring pgdesk up.
var (
	// ErrNoPool is returned by New when the pool is nil.
	ErrNoPool = errors.New("pgdesk: a non-nil *pgxpool.Pool is required")

	// ErrSecretRequired is returned by New when mutating routes are possible but
	// no signing key was configured. pgdesk never silently runs without CSRF (D5).
	ErrSecretRequired = errors.New("pgdesk: WithSecretKey is required when mutations are enabled")

	// ErrUnknownColumn is returned by resource setters (ListDisplay, Filters, …)
	// when a configured column name does not exist in the introspected table. It
	// surfaces configuration mistakes at New() time, fail-closed (D2).
	ErrUnknownColumn = errors.New("pgdesk: column does not exist on table")

	// ErrUnknownTable is returned by Resource when the named table/view is not in
	// the catalog for the configured schemas.
	ErrUnknownTable = errors.New("pgdesk: table not found in catalog")

	// ErrAmbiguousTable is returned by New when a resource name matches a table in
	// more than one configured schema. pgdesk refuses to guess which one was meant
	// (D2); narrow WithSchemas, or exclude the name from auto-registration.
	ErrAmbiguousTable = errors.New("pgdesk: table name is ambiguous across the configured schemas")

	// ErrUnsafeName is returned by New when a resource name cannot be a URL path
	// segment. The name is the resource's route, so it must be URL-safe.
	ErrUnsafeName = errors.New("pgdesk: resource name must be url-safe (letters, digits, - or _)")

	// ErrClosed is returned by methods called after Close.
	ErrClosed = errors.New("pgdesk: admin is closed")
)
