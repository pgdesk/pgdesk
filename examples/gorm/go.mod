// Standalone example: pgdesk serving an admin over a GORM-owned schema, sharing
// one pgxpool. It is a SEPARATE module on purpose -- GORM and its dependencies
// stay out of pgdesk's own go.mod, which keeps a single runtime dependency (pgx).
//
// pgdesk is resolved from the repo root via the replace directive below; once
// pgdesk is tagged, delete the replace and require a released version instead.
module github.com/pgdesk/pgdesk/examples/gorm

go 1.25.0

require (
	github.com/jackc/pgx/v5 v5.10.0
	github.com/pgdesk/pgdesk v0.0.0
	gorm.io/driver/postgres v1.6.0
	gorm.io/gorm v1.31.2
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	golang.org/x/sync v0.17.0 // indirect
	golang.org/x/text v0.29.0 // indirect
)

replace github.com/pgdesk/pgdesk => ../..
