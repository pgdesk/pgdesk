package introspect

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type TxBeginner interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

func Load(ctx context.Context, db TxBeginner, schemas []string) (*Catalog, error) {
	if len(schemas) == 0 {
		return nil, fmt.Errorf("pgdesk/introspect: at least one schema is required")
	}
	tx, err := db.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("pgdesk/introspect: begin snapshot tx: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	enums, err := loadEnums(ctx, tx, schemas)
	if err != nil {
		return nil, err
	}
	rels, relOrder, err := loadRelations(ctx, tx, schemas)
	if err != nil {
		return nil, err
	}
	if err := loadColumns(ctx, tx, schemas, rels, enums); err != nil {
		return nil, err
	}
	if err := loadPrimaryKeys(ctx, tx, schemas, rels); err != nil {
		return nil, err
	}
	if err := loadForeignKeys(ctx, tx, schemas, rels); err != nil {
		return nil, err
	}
	if err := loadUniqueConstraints(ctx, tx, schemas, rels); err != nil {
		return nil, err
	}

	tables := make([]*Table, 0, len(relOrder))
	for _, key := range relOrder {
		r := rels[key]
		t := NewTable(r.schema, r.name, r.isView, r.updatable, r.comment, r.cols, r.pk, r.fks)
		t.setCaps(r.insertable, r.updatable, r.deletable, r.hasXmin)
		t.setUniques(r.uniques)
		tables = append(tables, t)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("pgdesk/introspect: commit snapshot tx: %w", err)
	}
	return NewCatalog(schemas, tables), nil
}

type relBuild struct {
	oid        uint32
	schema     string
	name       string
	isView     bool
	insertable bool
	updatable  bool
	deletable  bool
	hasXmin    bool
	comment    string
	cols       []*Column
	byAttnum   map[int16]*Column
	pk         []*Column
	fks        []*ForeignKey
	uniques    []*UniqueConstraint
}

func relKey(schema, name string) string { return schema + "." + name }

func loadEnums(ctx context.Context, tx pgx.Tx, schemas []string) (map[uint32][]string, error) {
	const q = `
		SELECT e.enumtypid, e.enumlabel
		FROM pg_enum e
		JOIN pg_type t ON t.oid = e.enumtypid
		JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = ANY($1)
		ORDER BY e.enumtypid, e.enumsortorder`
	rows, err := tx.Query(ctx, q, schemas)
	if err != nil {
		return nil, fmt.Errorf("pgdesk/introspect: query enums: %w", err)
	}
	defer rows.Close()
	out := map[uint32][]string{}
	for rows.Next() {
		var oid uint32
		var label string
		if err := rows.Scan(&oid, &label); err != nil {
			return nil, fmt.Errorf("pgdesk/introspect: scan enum: %w", err)
		}
		out[oid] = append(out[oid], label)
	}
	return out, rows.Err()
}

func loadRelations(ctx context.Context, tx pgx.Tx, schemas []string) (map[string]*relBuild, []string, error) {
	const q = `
		SELECT c.oid, n.nspname, c.relname, c.relkind,
		       COALESCE(obj_description(c.oid, 'pg_class'), '') AS comment,
		       (pg_relation_is_updatable(c.oid, true) & 8)  = 8  AS insertable,
		       (pg_relation_is_updatable(c.oid, true) & 4)  = 4  AS updatable,
		       (pg_relation_is_updatable(c.oid, true) & 16) = 16 AS deletable,
		       c.relkind IN ('r','p','m') AS has_xmin
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = ANY($1)
		  AND c.relkind IN ('r','p','v','m','f')
		ORDER BY n.nspname, c.relname`
	rows, err := tx.Query(ctx, q, schemas)
	if err != nil {
		return nil, nil, fmt.Errorf("pgdesk/introspect: query relations: %w", err)
	}
	defer rows.Close()
	rels := map[string]*relBuild{}
	var order []string
	for rows.Next() {
		var (
			oid                              uint32
			schema                           string
			name                             string
			relkind                          string
			comment                          string
			insertable, updatable, deletable bool
			hasXmin                          bool
		)
		if err := rows.Scan(&oid, &schema, &name, &relkind, &comment,
			&insertable, &updatable, &deletable, &hasXmin); err != nil {
			return nil, nil, fmt.Errorf("pgdesk/introspect: scan relation: %w", err)
		}
		key := relKey(schema, name)
		rels[key] = &relBuild{
			oid:        oid,
			schema:     schema,
			name:       name,
			isView:     relkind == "v" || relkind == "m",
			insertable: insertable,
			updatable:  updatable,
			deletable:  deletable,
			hasXmin:    hasXmin,
			comment:    comment,
			byAttnum:   map[int16]*Column{},
		}
		order = append(order, key)
	}
	return rels, order, rows.Err()
}

func loadColumns(ctx context.Context, tx pgx.Tx, schemas []string, rels map[string]*relBuild, enums map[uint32][]string) error {

	const q = `
		SELECT n.nspname, c.relname,
		       a.attname, a.attnum,
		       CASE WHEN t.typtype = 'd' THEN t.typbasetype ELSE a.atttypid END AS eff_typoid,
		       CASE WHEN t.typtype = 'd' THEN bt.typname      ELSE t.typname      END AS eff_typname,
		       CASE WHEN t.typtype = 'd' THEN bt.typcategory  ELSE t.typcategory  END AS eff_typcategory,
		       CASE WHEN t.typtype = 'd' THEN bt.typtype       ELSE t.typtype       END AS eff_typtype,
		       a.attnotnull,
		       a.atthasdef,
		       (a.attgenerated <> '') AS is_generated,
		       (a.attidentity = 'a') AS is_identity_always,
		       COALESCE(col_description(c.oid, a.attnum), '') AS comment
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_type t ON t.oid = a.atttypid
		LEFT JOIN pg_type bt ON bt.oid = t.typbasetype
		WHERE n.nspname = ANY($1)
		  AND c.relkind IN ('r','p','v','m','f')
		  AND a.attnum > 0
		  AND NOT a.attisdropped
		ORDER BY n.nspname, c.relname, a.attnum`
	rows, err := tx.Query(ctx, q, schemas)
	if err != nil {
		return fmt.Errorf("pgdesk/introspect: query columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			schema, relname                        string
			attname                                string
			attnum                                 int16
			atttypid                               uint32
			typname                                string
			typcategory, typtype                   string
			notnull, hasdef, generated, identityAl bool
			comment                                string
		)
		if err := rows.Scan(&schema, &relname, &attname, &attnum, &atttypid,
			&typname, &typcategory, &typtype, &notnull, &hasdef, &generated, &identityAl, &comment); err != nil {
			return fmt.Errorf("pgdesk/introspect: scan column: %w", err)
		}
		r, ok := rels[relKey(schema, relname)]
		if !ok {
			continue
		}
		isEnum := typtype == "e"
		cat := categoryFor(typname, byteOf(typcategory), isEnum)
		col := &Column{
			Name:             attname,
			Position:         int(attnum),
			DataType:         typname,
			Category:         cat,
			Nullable:         !notnull,
			HasDefault:       hasdef,
			IsGenerated:      generated,
			IsIdentityAlways: identityAl,
			Comment:          comment,
		}
		if isEnum {
			col.EnumLabels = enums[atttypid]
		}
		r.cols = append(r.cols, col)
		r.byAttnum[attnum] = col
	}
	return rows.Err()
}

func loadPrimaryKeys(ctx context.Context, tx pgx.Tx, schemas []string, rels map[string]*relBuild) error {
	const q = `
		SELECT n.nspname, c.relname, a.attname,
		       array_position(i.indkey::int2[], a.attnum) AS ord
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
		WHERE i.indisprimary AND n.nspname = ANY($1)
		ORDER BY n.nspname, c.relname, ord`
	rows, err := tx.Query(ctx, q, schemas)
	if err != nil {
		return fmt.Errorf("pgdesk/introspect: query primary keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema, relname, attname string
		var ord int
		if err := rows.Scan(&schema, &relname, &attname, &ord); err != nil {
			return fmt.Errorf("pgdesk/introspect: scan primary key: %w", err)
		}
		r, ok := rels[relKey(schema, relname)]
		if !ok {
			continue
		}
		for _, c := range r.cols {
			if c.Name == attname {
				r.pk = append(r.pk, c)
				break
			}
		}
	}
	return rows.Err()
}

func loadForeignKeys(ctx context.Context, tx pgx.Tx, schemas []string, rels map[string]*relBuild) error {
	const q = `
		SELECT n.nspname, c.relname,
		       con.conkey, con.confkey,
		       fn.nspname AS ref_schema, fc.relname AS ref_table
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_class fc ON fc.oid = con.confrelid
		JOIN pg_namespace fn ON fn.oid = fc.relnamespace
		WHERE con.contype = 'f' AND n.nspname = ANY($1)
		ORDER BY n.nspname, c.relname, con.conname`
	rows, err := tx.Query(ctx, q, schemas)
	if err != nil {
		return fmt.Errorf("pgdesk/introspect: query foreign keys: %w", err)
	}
	defer rows.Close()

	type pendingFK struct {
		rel       *relBuild
		conkey    []int16
		confkey   []int16
		refSchema string
		refTable  string
	}
	var pending []pendingFK
	for rows.Next() {
		var schema, relname, refSchema, refTable string
		var conkey, confkey []int16
		if err := rows.Scan(&schema, &relname, &conkey, &confkey, &refSchema, &refTable); err != nil {
			return fmt.Errorf("pgdesk/introspect: scan foreign key: %w", err)
		}
		r, ok := rels[relKey(schema, relname)]
		if !ok {
			continue
		}
		pending = append(pending, pendingFK{r, conkey, confkey, refSchema, refTable})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range pending {
		fk := &ForeignKey{RefSchema: p.refSchema, RefTable: p.refTable}
		for _, an := range p.conkey {
			if c := p.rel.byAttnum[an]; c != nil {
				fk.Columns = append(fk.Columns, c.Name)
			}
		}
		if ref := rels[relKey(p.refSchema, p.refTable)]; ref != nil {
			for _, an := range p.confkey {
				if c := ref.byAttnum[an]; c != nil {
					fk.RefColumns = append(fk.RefColumns, c.Name)
				}
			}
		}

		if len(fk.Columns) > 0 && len(fk.Columns) == len(fk.RefColumns) {
			p.rel.fks = append(p.rel.fks, fk)
		}
	}
	return nil
}

func loadUniqueConstraints(ctx context.Context, tx pgx.Tx, schemas []string, rels map[string]*relBuild) error {
	const q = `
		SELECT n.nspname, c.relname, ic.relname AS index_name, i.indkey::int2[]
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE i.indisunique AND NOT i.indisprimary AND n.nspname = ANY($1)
		ORDER BY n.nspname, c.relname, ic.relname`
	rows, err := tx.Query(ctx, q, schemas)
	if err != nil {
		return fmt.Errorf("pgdesk/introspect: query unique constraints: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema, relname, idxName string
		var attnums []int16
		if err := rows.Scan(&schema, &relname, &idxName, &attnums); err != nil {
			return fmt.Errorf("pgdesk/introspect: scan unique constraint: %w", err)
		}
		r, ok := rels[relKey(schema, relname)]
		if !ok {
			continue
		}
		uc := &UniqueConstraint{Name: idxName}
		for _, an := range attnums {
			if an == 0 {
				continue
			}
			if col := r.byAttnum[an]; col != nil {
				uc.Columns = append(uc.Columns, col.Name)
			}
		}
		if len(uc.Columns) > 0 {
			r.uniques = append(r.uniques, uc)
		}
	}
	return rows.Err()
}

func byteOf(s string) byte {
	if s == "" {
		return 0
	}
	return s[0]
}
