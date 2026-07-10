package pgdesk

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// mappedError translates a PostgreSQL error into inline, user-facing feedback.
// PostgreSQL is the validator (D7): pgdesk does not re-implement uniqueness, FK,
// nullability, or check logic in Go.
type mappedError struct {
	// fieldErrors maps a column name to a message rendered next to that field.
	fieldErrors map[string]string
	// formError is a general message shown when no specific column applies.
	formError string
}

func (m mappedError) empty() bool {
	return len(m.fieldErrors) == 0 && m.formError == ""
}

// uniqueLookup resolves a unique constraint/index name to the columns it covers,
// so a 23505 violation attaches to the specific field(s) (D7). It is satisfied by
// *introspect.Table.UniqueColumns.
type uniqueLookup func(constraintName string) ([]string, bool)

// mapPgError classifies a *pgconn.PgError by SQLSTATE into field/form errors
// (D7). constraintMsgs supplies operator-friendly overrides keyed by constraint
// name (WithConstraintMessage); uniqueCols resolves a unique constraint to its
// columns (may be nil). A non-Postgres error yields a generic form error with no
// internal detail (F5: internals never reach the browser).
func mapPgError(err error, constraintMsgs map[string]string, uniqueCols uniqueLookup) mappedError {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return mappedError{formError: "The change could not be saved. Please try again."}
	}

	// Operator override by constraint name wins for any classified constraint.
	if msg, ok := constraintMsgs[pg.ConstraintName]; ok && msg != "" {
		if cols := uniqueColumnsFor(pg, uniqueCols); len(cols) > 0 {
			return fieldErrorsFor(cols, msg)
		}
		if pg.ColumnName != "" {
			return mappedError{fieldErrors: map[string]string{pg.ColumnName: msg}}
		}
		return mappedError{formError: msg}
	}

	switch pg.Code {
	case "23505": // unique_violation
		// Postgres omits ColumnName for unique violations, so resolve the
		// constraint's columns from the catalog and attach to each (D7).
		if cols := uniqueColumnsFor(pg, uniqueCols); len(cols) > 0 {
			return fieldErrorsFor(cols, "must be unique")
		}
		return fieldOrForm(pg.ColumnName, "must be unique",
			"A record with these values already exists.")
	case "23502": // not_null_violation
		return fieldOrForm(pg.ColumnName, "is required",
			"A required value is missing.")
	case "23503": // foreign_key_violation
		return fieldOrForm(pg.ColumnName, "referenced record does not exist",
			"A referenced record does not exist.")
	case "23514": // check_violation
		return mappedError{formError: "A value violates a constraint on this record."}
	case "22P02": // invalid_text_representation
		return fieldOrForm(pg.ColumnName, "invalid value",
			"One of the values has an invalid format.")
	case "428C9": // generated_always -- wrote a value to a GENERATED ALWAYS column
		// pgdesk excludes generated and identity-always columns from the editable
		// set, so this should be unreachable; if a schema surprise lets one through,
		// fail with an actionable message rather than a generic one.
		return fieldOrForm(pg.ColumnName, "is managed by the database and cannot be set",
			"A database-managed column cannot be set directly.")
	default:
		return mappedError{formError: "The change could not be saved. Please try again."}
	}
}

func fieldOrForm(column, fieldMsg, formMsg string) mappedError {
	if column != "" {
		return mappedError{fieldErrors: map[string]string{column: fieldMsg}}
	}
	return mappedError{formError: formMsg}
}

// uniqueColumnsFor resolves the columns of the violated unique constraint, or nil.
func uniqueColumnsFor(pg *pgconn.PgError, lookup uniqueLookup) []string {
	if lookup == nil || pg.ConstraintName == "" {
		return nil
	}
	cols, _ := lookup(pg.ConstraintName)
	return cols
}

// fieldErrorsFor attaches the same message to every named column.
func fieldErrorsFor(cols []string, msg string) mappedError {
	fe := make(map[string]string, len(cols))
	for _, c := range cols {
		fe[c] = msg
	}
	return mappedError{fieldErrors: fe}
}
