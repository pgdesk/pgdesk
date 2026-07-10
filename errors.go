package pgdesk

import "errors"

// Sentinel errors returned by construction and configuration. Request-path
// errors are handled internally and never leak to the browser; these are for
// the host wiring pgdesk up.
var (
	// ErrNoPool is returned by New when the pool is nil.
	ErrNoPool = errors.New("pgdesk: a non-nil *pgxpool.Pool is required")

	// ErrSecretRequired is returned by New when mutating routes are possible but
	// no signing key was configured. pgdesk never silently runs without CSRF.
	ErrSecretRequired = errors.New("pgdesk: WithSecretKey is required when mutations are enabled")

	// ErrUnknownColumn is returned by resource setters (ListDisplay, Filters, ...)
	// when a configured column name does not exist in the introspected table. It
	// surfaces configuration mistakes at New() time, fail-closed.
	ErrUnknownColumn = errors.New("pgdesk: column does not exist on table")

	// ErrUnknownTable is returned by Resource when the named table/view is not in
	// the catalog for the configured schemas.
	ErrUnknownTable = errors.New("pgdesk: table not found in catalog")

	// ErrAmbiguousTable is returned by New when a resource name matches a table in
	// more than one configured schema. pgdesk refuses to guess which one was meant;
	// narrow WithSchemas, or exclude the name from auto-registration.
	ErrAmbiguousTable = errors.New("pgdesk: table name is ambiguous across the configured schemas")

	// ErrUnsafeName is returned by New when a resource name cannot be a URL path
	// segment. The name is the resource's route, so it must be URL-safe.
	ErrUnsafeName = errors.New("pgdesk: resource name must be url-safe (letters, digits, - or _)")

	// ErrUnsafeBasePath is returned by New when WithBasePath is given a value that
	// cannot form a valid mux pattern (e.g. it contains '{', '}', whitespace, or
	// control characters). Validating at construction keeps the "New never panics"
	// contract: a malformed base path would otherwise panic later in Mount when
	// registered on an http.ServeMux.
	ErrUnsafeBasePath = errors.New("pgdesk: base path must be a clean URL path (no '{', '}', whitespace, or control characters)")

	// ErrKeyTypeMismatch is returned by Keys.Int64s/Strings when the resource's key
	// column exists and is single, but its type category does not match the accessor
	// (e.g. calling Int64s on a text key). Wrapped so callers can errors.Is it.
	ErrKeyTypeMismatch = errors.New("pgdesk: bulk-action key type does not match the requested accessor")

	// ErrKeyShapeMismatch is returned by Keys.Int64s/Strings when the resource key
	// is not a single column (a composite key); use Keys.Raw instead. Wrapped so
	// callers can errors.Is it.
	ErrKeyShapeMismatch = errors.New("pgdesk: bulk-action key is not a single column; use Keys.Raw")

	// ErrClosed is returned by methods called after Close.
	ErrClosed = errors.New("pgdesk: admin is closed")
)
