package pgdesk

import "errors"

var (
	// ErrNoPool is returned by New and NewWithDB when given a nil pool or DB.
	ErrNoPool = errors.New("pgdesk: a non-nil *pgxpool.Pool is required")

	// ErrSecretRequired is returned by New when a resource is writable or has actions but WithSecretKey is not set.
	ErrSecretRequired = errors.New("pgdesk: WithSecretKey is required when mutations are enabled")

	// ErrUnknownColumn is returned when a Resource setting or scope Constraint names a column the table does not have.
	ErrUnknownColumn = errors.New("pgdesk: column does not exist on table")

	// ErrUnsearchableColumn is returned when SearchFields names a column that is neither text nor a single-column foreign key.
	ErrUnsearchableColumn = errors.New("pgdesk: search column must be text or a single-column foreign key")

	// ErrUnknownTable is returned when WithResource names a table that is not in the configured schemas.
	ErrUnknownTable = errors.New("pgdesk: table not found in catalog")

	// ErrAmbiguousTable is returned when a table name exists in more than one configured schema.
	ErrAmbiguousTable = errors.New("pgdesk: table name is ambiguous across the configured schemas")

	// ErrUnsafeName is returned when a resource name has characters other than letters, digits, - and _.
	ErrUnsafeName = errors.New("pgdesk: resource name must be url-safe (letters, digits, - or _)")

	// ErrUnsafeBasePath is returned by New when the base path has characters other than letters, digits, /, - and _.
	ErrUnsafeBasePath = errors.New("pgdesk: base path must be a clean URL path (no '{', '}', whitespace, or control characters)")

	// ErrKeyTypeMismatch is returned by a Keys accessor that does not match the key column's type.
	ErrKeyTypeMismatch = errors.New("pgdesk: bulk-action key type does not match the requested accessor")

	// ErrKeyShapeMismatch is returned by Keys.Int64s and Keys.Strings when the key is not a single column.
	ErrKeyShapeMismatch = errors.New("pgdesk: bulk-action key is not a single column; use Keys.Raw")

	// ErrClosed is returned by Reload and Healthy after Close.
	ErrClosed = errors.New("pgdesk: admin is closed")
)
