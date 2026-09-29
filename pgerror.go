package pgdesk

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

type mappedError struct {
	fieldErrors map[string]string

	formError string
}

func (m mappedError) empty() bool {
	return len(m.fieldErrors) == 0 && m.formError == ""
}

type uniqueLookup func(constraintName string) ([]string, bool)

func mapPgError(err error, constraintMsgs map[string]string, uniqueCols uniqueLookup) mappedError {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return mappedError{formError: "The change could not be saved. Please try again."}
	}

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
	case "23505":

		if cols := uniqueColumnsFor(pg, uniqueCols); len(cols) > 0 {
			return fieldErrorsFor(cols, "must be unique")
		}
		return fieldOrForm(pg.ColumnName, "must be unique",
			"A record with these values already exists.")
	case "23502":
		return fieldOrForm(pg.ColumnName, "is required",
			"A required value is missing.")
	case "23503":
		return fieldOrForm(pg.ColumnName, "referenced record does not exist",
			"A referenced record does not exist.")
	case "23514":
		return mappedError{formError: "A value violates a constraint on this record."}
	case "22P02":
		return fieldOrForm(pg.ColumnName, "invalid value",
			"One of the values has an invalid format.")
	case "428C9":

		return fieldOrForm(pg.ColumnName, "is managed by the database and cannot be set",
			"A database-managed column cannot be set directly.")
	default:
		return mappedError{formError: "The change could not be saved. Please try again."}
	}
}

func isForeignKeyViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23503"
}

func fieldOrForm(column, fieldMsg, formMsg string) mappedError {
	if column != "" {
		return mappedError{fieldErrors: map[string]string{column: fieldMsg}}
	}
	return mappedError{formError: formMsg}
}

func uniqueColumnsFor(pg *pgconn.PgError, lookup uniqueLookup) []string {
	if lookup == nil || pg.ConstraintName == "" {
		return nil
	}
	cols, _ := lookup(pg.ConstraintName)
	return cols
}

func fieldErrorsFor(cols []string, msg string) mappedError {
	fe := make(map[string]string, len(cols))
	for _, c := range cols {
		fe[c] = msg
	}
	return mappedError{fieldErrors: fe}
}
