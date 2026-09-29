// Package pgdesk serves an admin UI for a PostgreSQL database as an http.Handler.
//
// It reads the schema from pg_catalog and serves pages to browse and edit the rows of
// each table registered with WithResource or WithAutoRegister.
package pgdesk
