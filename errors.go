package pgdesk

import "errors"

var (
	ErrNoPool = errors.New("pgdesk: a non-nil *pgxpool.Pool is required")

	ErrSecretRequired = errors.New("pgdesk: WithSecretKey is required when mutations are enabled")

	ErrUnknownColumn = errors.New("pgdesk: column does not exist on table")

	ErrUnknownTable = errors.New("pgdesk: table not found in catalog")

	ErrAmbiguousTable = errors.New("pgdesk: table name is ambiguous across the configured schemas")

	ErrUnsafeName = errors.New("pgdesk: resource name must be url-safe (letters, digits, - or _)")

	ErrUnsafeBasePath = errors.New("pgdesk: base path must be a clean URL path (no '{', '}', whitespace, or control characters)")

	ErrKeyTypeMismatch = errors.New("pgdesk: bulk-action key type does not match the requested accessor")

	ErrKeyShapeMismatch = errors.New("pgdesk: bulk-action key is not a single column; use Keys.Raw")

	ErrClosed = errors.New("pgdesk: admin is closed")
)
