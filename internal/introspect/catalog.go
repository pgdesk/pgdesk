package introspect

import (
	"slices"
	"strings"
)

type TypeCategory string

const (
	CatText      TypeCategory = "text"
	CatNumeric   TypeCategory = "numeric"
	CatTimestamp TypeCategory = "timestamp"
	CatBool      TypeCategory = "bool"
	CatEnum      TypeCategory = "enum"
	CatUUID      TypeCategory = "uuid"
	CatJSON      TypeCategory = "json"
	CatOther     TypeCategory = "other"
)

type Column struct {
	Name string

	Position int

	DataType string

	Category TypeCategory

	Nullable bool

	HasDefault bool

	IsGenerated bool

	IsIdentityAlways bool

	EnumLabels []string

	Comment string
}

func (c *Column) IsEnum() bool { return c.Category == CatEnum && len(c.EnumLabels) > 0 }

func (c *Column) ValidEnumLabel(v string) bool {
	return slices.Contains(c.EnumLabels, v)
}

type ForeignKey struct {
	Columns []string

	RefSchema string
	RefTable  string

	RefColumns []string
}

type UniqueConstraint struct {
	Name    string
	Columns []string
}

type Table struct {
	Schema string
	Name   string

	IsView bool

	Updatable bool

	Insertable bool

	Deletable bool

	HasXmin bool

	Comment string

	columns []*Column

	byName map[string]*Column

	PrimaryKey []*Column

	ForeignKeys []*ForeignKey

	UniqueConstraints []*UniqueConstraint
	uniqueByName      map[string][]string
}

func (t *Table) setUniques(u []*UniqueConstraint) {
	t.UniqueConstraints = u
	t.uniqueByName = make(map[string][]string, len(u))
	for _, uc := range u {
		t.uniqueByName[uc.Name] = uc.Columns
	}
}

func (t *Table) UniqueColumns(constraintName string) ([]string, bool) {
	cols, ok := t.uniqueByName[constraintName]
	return cols, ok
}

func (t *Table) QualifiedName() string { return t.Schema + "." + t.Name }

func (t *Table) Columns() []*Column { return t.columns }

func (t *Table) Column(name string) (*Column, bool) {
	c, ok := t.byName[name]
	return c, ok
}

func (t *Table) HasKey() bool { return len(t.PrimaryKey) > 0 }

func NewTable(schema, name string, isView, updatable bool, comment string, cols, pk []*Column, fks []*ForeignKey) *Table {
	byName := make(map[string]*Column, len(cols))
	for _, c := range cols {
		byName[c.Name] = c
	}
	return &Table{
		Schema:      schema,
		Name:        name,
		IsView:      isView,
		Updatable:   updatable,
		Insertable:  updatable,
		Deletable:   updatable,
		HasXmin:     !isView,
		Comment:     comment,
		columns:     cols,
		byName:      byName,
		PrimaryKey:  pk,
		ForeignKeys: fks,
	}
}

func (t *Table) setCaps(insertable, updatable, deletable, hasXmin bool) {
	t.Insertable = insertable
	t.Updatable = updatable
	t.Deletable = deletable
	t.HasXmin = hasXmin
}

type Catalog struct {
	Schemas []string

	tables map[string]*Table

	order []*Table
}

func NewCatalog(schemas []string, tables []*Table) *Catalog {
	m := make(map[string]*Table, len(tables))
	order := make([]*Table, 0, len(tables))
	for _, t := range tables {
		m[t.Schema+"."+t.Name] = t
		order = append(order, t)
	}
	return &Catalog{Schemas: schemas, tables: m, order: order}
}

func (c *Catalog) Table(schema, name string) (*Table, bool) {
	t, ok := c.tables[schema+"."+name]
	return t, ok
}

func (c *Catalog) Tables() []*Table { return c.order }

func categoryFor(name string, typcat byte, isEnum bool) TypeCategory {
	if isEnum || typcat == 'E' {
		return CatEnum
	}
	switch name {
	case "bool", "boolean":
		return CatBool
	case "uuid":
		return CatUUID
	case "json", "jsonb":
		return CatJSON
	}

	switch {
	case name == "date",
		strings.HasPrefix(name, "timestamp"),
		strings.HasPrefix(name, "time"):
		return CatTimestamp
	}
	switch typcat {
	case 'B':
		return CatBool
	case 'N':
		return CatNumeric
	case 'D':
		return CatTimestamp
	case 'S':
		return CatText
	}
	return CatOther
}
