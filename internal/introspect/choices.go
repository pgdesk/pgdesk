package introspect

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var (
	checkAnyArray = regexp.MustCompile(`^CHECK \(+(?:\(?[a-z_][a-z0-9_]*\)?(?:::[a-z ]+)?) = ANY \(+ARRAY\[(.*?)\](?:\)::[a-z ]+\[\])?\)+$`)
	checkLiteral  = regexp.MustCompile(`^'((?:[^']|'')*)'::[a-z ]+(?:\(\d+\))?$`)
)

func parseCheckChoices(def string) []string {
	m := checkAnyArray.FindStringSubmatch(def)
	if m == nil || strings.Contains(m[1], "ARRAY[") {
		return nil
	}
	var out []string
	for _, part := range splitArrayElems(m[1]) {
		lm := checkLiteral.FindStringSubmatch(strings.TrimSpace(part))
		if lm == nil {
			return nil
		}
		out = append(out, strings.ReplaceAll(lm[1], "''", "'"))
	}
	return out
}

func splitArrayElems(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'':
			inQuote = !inQuote
			cur.WriteByte(ch)
		case ch == ',' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(ch)
		}
	}
	return append(parts, cur.String())
}

func loadCheckChoices(ctx context.Context, tx pgx.Tx, schemas []string, rels map[string]*relBuild) error {
	const q = `
		SELECT n.nspname, c.relname, con.conkey[1], pg_get_constraintdef(con.oid)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE con.contype = 'c' AND array_length(con.conkey, 1) = 1 AND n.nspname = ANY($1)
		ORDER BY n.nspname, c.relname, con.conname`
	rows, err := tx.Query(ctx, q, schemas)
	if err != nil {
		return fmt.Errorf("pgdesk/introspect: query check constraints: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema, relname, def string
		var attnum int16
		if err := rows.Scan(&schema, &relname, &attnum, &def); err != nil {
			return fmt.Errorf("pgdesk/introspect: scan check constraint: %w", err)
		}
		r, ok := rels[relKey(schema, relname)]
		if !ok {
			continue
		}
		col := r.byAttnum[attnum]
		if col == nil || col.Category != CatText || len(col.Choices) > 0 {
			continue
		}
		col.Choices = parseCheckChoices(def)
	}
	return rows.Err()
}
