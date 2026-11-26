package pg

import (
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/makiuchi-d/sql-migration/dbstate"
	"github.com/makiuchi-d/sql-migration/dialect"
)

// quoteIdent quotes an identifier as PostgreSQL does.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteLiteral quotes a string as PostgreSQL does (standard_conforming_strings = on).
func quoteLiteral(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}

const qHasMigrationTable = `
SELECT EXISTS (
  SELECT 1 FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
   WHERE n.nspname = current_schema() AND c.relname = '_migrations' AND c.relkind = 'r')`

func CheckMigrationTable(db *sqlx.DB) error {
	var ok bool
	if err := db.Get(&ok, qHasMigrationTable); err != nil {
		return err
	}
	if !ok {
		return dialect.ErrNoMigrationTable
	}
	return nil
}

// TakeSnapshot reads the tables, their records and the sequences of the
// current schema.
func TakeSnapshot(db *sqlx.DB) (*dbstate.Snapshot, error) {
	seqs, err := getSequences(db)
	if err != nil {
		return nil, err
	}
	tbls, err := getTables(db, seqs)
	if err != nil {
		return nil, err
	}

	ss := &dbstate.Snapshot{
		Records: make(map[string]*dbstate.Records, len(tbls)),
	}
	for _, s := range seqs {
		ss.Objects = append(ss.Objects, s.object())
	}
	for _, t := range tbls {
		ss.Objects = append(ss.Objects, t)
		rec, err := getRecords(db, t.Name)
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", t.Name, err)
		}
		ss.Records[t.Name] = rec
	}

	ss.Objects, err = dbstate.SortObjects(ss.Objects)
	if err != nil {
		return nil, err
	}
	return ss, nil
}

// ---- sequences

// sequence is a sequence with its current counter.
// An identity sequence is created by the column definition, so only its
// counter is restored, after the table.
type sequence struct {
	Name        string         `db:"name"`
	Type        string         `db:"type"`
	Start       int64          `db:"start"`
	Increment   int64          `db:"increment"`
	Min         int64          `db:"min"`
	Max         int64          `db:"max"`
	Cache       int64          `db:"cache"`
	Cycle       bool           `db:"cycle"`
	Identity    bool           `db:"identity"`
	OwnerTable  sql.NullString `db:"owner_table"`
	OwnerColumn sql.NullString `db:"owner_column"`
	Last        int64          `db:"last_value"`
	IsCalled    bool           `db:"is_called"`
}

func (s *sequence) create() string {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE SEQUENCE %s AS %s", quoteIdent(s.Name), s.Type)
	fmt.Fprintf(&b, " START WITH %d INCREMENT BY %d", s.Start, s.Increment)
	fmt.Fprintf(&b, " MINVALUE %d MAXVALUE %d CACHE %d", s.Min, s.Max, s.Cache)
	if s.Cycle {
		b.WriteString(" CYCLE")
	}
	return b.String()
}

// ownedBy returns the statement that ties the sequence to its column.
func (s *sequence) ownedBy() string {
	return fmt.Sprintf("ALTER SEQUENCE %s OWNED BY %s.%s",
		quoteIdent(s.Name), quoteIdent(s.OwnerTable.String), quoteIdent(s.OwnerColumn.String))
}

// setVal returns the statement that restores the counter.
func (s *sequence) setVal() string {
	return fmt.Sprintf("SELECT setval(%s, %d, %v)",
		quoteLiteral(quoteIdent(s.Name)), s.Last, s.IsCalled)
}

func (s *sequence) object() *dbstate.Object {
	o := &dbstate.Object{
		Kind: dbstate.KindSequence,
		Name: s.Name,
	}
	if s.Identity {
		o.Stmts = []string{s.setVal()}
		o.Deps = []dbstate.Ref{{Kind: dbstate.KindTable, Name: s.OwnerTable.String}}
	} else {
		o.Stmts = []string{s.create(), s.setVal()}
	}
	return o
}

const qSequences = `
SELECT c.relname AS name,
       format_type(s.seqtypid, NULL) AS type,
       s.seqstart AS start, s.seqincrement AS increment,
       s.seqmin AS min, s.seqmax AS max, s.seqcache AS cache, s.seqcycle AS cycle,
       coalesce(a.attidentity, '') <> '' AS identity,
       tc.relname AS owner_table,
       a.attname AS owner_column
  FROM pg_sequence s
  JOIN pg_class c ON c.oid = s.seqrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
  LEFT JOIN pg_depend d ON d.classid = 'pg_class'::regclass AND d.objid = c.oid
                       AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a', 'i')
  LEFT JOIN pg_class tc ON tc.oid = d.refobjid
                       AND tc.relkind = 'r' AND tc.relpersistence = 'p'
  LEFT JOIN pg_attribute a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
 WHERE n.nspname = current_schema()
 ORDER BY c.relname`

// getSequences returns every sequence of the schema with its current counter.
// The counter is read from the sequence itself: deriving it from the table
// data would lose a counter set by setval or advanced past the rows.
func getSequences(db *sqlx.DB) ([]*sequence, error) {
	var seqs []*sequence
	if err := db.Select(&seqs, qSequences); err != nil {
		return nil, err
	}
	for _, s := range seqs {
		q := fmt.Sprintf("SELECT last_value, is_called FROM %s", quoteIdent(s.Name))
		if err := db.Get(s, q); err != nil {
			return nil, err
		}
	}
	return seqs, nil
}

// ---- tables

type column struct {
	Table     string         `db:"table"`
	Name      string         `db:"name"`
	Type      string         `db:"type"`
	NotNull   bool           `db:"not_null"`
	Identity  string         `db:"identity"` // "" | "a" (always) | "d" (by default)
	Generated bool           `db:"generated"`
	Default   sql.NullString `db:"default_expr"`
}

// def returns the column definition in a CREATE TABLE statement.
func (c *column) def() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", quoteIdent(c.Name), c.Type)
	switch c.Identity {
	case "a":
		b.WriteString(" GENERATED ALWAYS AS IDENTITY")
	case "d":
		b.WriteString(" GENERATED BY DEFAULT AS IDENTITY")
	}
	if c.Default.Valid {
		if c.Generated {
			fmt.Fprintf(&b, " GENERATED ALWAYS AS (%s) STORED", c.Default.String)
		} else {
			fmt.Fprintf(&b, " DEFAULT %s", c.Default.String)
		}
	}
	if c.NotNull {
		b.WriteString(" NOT NULL")
	}
	return b.String()
}

type constraint struct {
	Table    string         `db:"table"`
	Name     string         `db:"name"`
	Def      string         `db:"def"`
	RefTable sql.NullString `db:"ref_table"`
}

type index struct {
	Table string `db:"table"`
	Def   string `db:"def"`
}

type seqUse struct {
	Table    string `db:"table"`
	Sequence string `db:"sequence"`
}

const qTables = `
SELECT c.relname
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = current_schema() AND c.relkind = 'r' AND c.relpersistence = 'p'
 ORDER BY c.relname`

const qColumns = `
SELECT c.relname AS "table",
       a.attname AS name,
       format_type(a.atttypid, a.atttypmod) AS type,
       a.attnotnull AS not_null,
       a.attidentity AS identity,
       a.attgenerated <> '' AS generated,
       pg_get_expr(ad.adbin, ad.adrelid) AS default_expr
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_attribute a ON a.attrelid = c.oid
  LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
 WHERE n.nspname = current_schema() AND c.relkind = 'r' AND c.relpersistence = 'p'
   AND a.attnum > 0 AND NOT a.attisdropped
 ORDER BY c.relname, a.attnum`

const qConstraints = `
SELECT c.relname AS "table",
       con.conname AS name,
       pg_get_constraintdef(con.oid) AS def,
       fc.relname AS ref_table
  FROM pg_constraint con
  JOIN pg_class c ON c.oid = con.conrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
  LEFT JOIN pg_class fc ON fc.oid = con.confrelid
 WHERE n.nspname = current_schema() AND con.contype IN ('p', 'u', 'f', 'c', 'x')
 ORDER BY c.relname, con.contype, con.conname`

const qIndexes = `
SELECT c.relname AS "table",
       pg_get_indexdef(i.indexrelid) AS def
  FROM pg_index i
  JOIN pg_class ic ON ic.oid = i.indexrelid
  JOIN pg_class c ON c.oid = i.indrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
 WHERE n.nspname = current_schema() AND c.relkind = 'r' AND c.relpersistence = 'p'
   AND NOT EXISTS (SELECT 1 FROM pg_constraint con WHERE con.conindid = i.indexrelid)
 ORDER BY c.relname, ic.relname`

// sequences used by the column defaults (nextval)
const qSequenceUses = `
SELECT DISTINCT c.relname AS "table", sc.relname AS "sequence"
  FROM pg_attrdef ad
  JOIN pg_class c ON c.oid = ad.adrelid
  JOIN pg_namespace n ON n.oid = c.relnamespace
  JOIN pg_depend d ON d.classid = 'pg_attrdef'::regclass AND d.objid = ad.oid
                  AND d.refclassid = 'pg_class'::regclass
  JOIN pg_class sc ON sc.oid = d.refobjid AND sc.relkind = 'S'
 WHERE n.nspname = current_schema()
 ORDER BY 1, 2`

// getTables reconstructs the tables from the system catalogs.
// PostgreSQL has no "SHOW CREATE TABLE". The statements of a table are the
// CREATE TABLE, the ownership of its serial sequences and the indexes not
// backed by a constraint.
func getTables(db *sqlx.DB, seqs []*sequence) ([]*dbstate.Object, error) {
	var names []string
	if err := db.Select(&names, qTables); err != nil {
		return nil, err
	}
	tblmap := make(map[string]*dbstate.Object, len(names))
	for _, n := range names {
		tblmap[n] = &dbstate.Object{Kind: dbstate.KindTable, Name: n}
	}
	addDep := func(t *dbstate.Object, ref dbstate.Ref) {
		if ref != t.Ref() && !slices.Contains(t.Deps, ref) {
			t.Deps = append(t.Deps, ref)
		}
	}

	var cols []*column
	if err := db.Select(&cols, qColumns); err != nil {
		return nil, err
	}
	colmap := make(map[string][]*column, len(names))
	for _, c := range cols {
		colmap[c.Table] = append(colmap[c.Table], c)
	}

	var cons []*constraint
	if err := db.Select(&cons, qConstraints); err != nil {
		return nil, err
	}
	conmap := make(map[string][]*constraint, len(names))
	for _, c := range cons {
		t, ok := tblmap[c.Table]
		if !ok {
			continue
		}
		conmap[c.Table] = append(conmap[c.Table], c)
		if c.RefTable.Valid {
			addDep(t, dbstate.Ref{Kind: dbstate.KindTable, Name: c.RefTable.String})
		}
	}

	var idxs []*index
	if err := db.Select(&idxs, qIndexes); err != nil {
		return nil, err
	}
	idxmap := make(map[string][]string, len(names))
	for _, i := range idxs {
		idxmap[i.Table] = append(idxmap[i.Table], i.Def)
	}

	var uses []seqUse
	if err := db.Select(&uses, qSequenceUses); err != nil {
		return nil, err
	}
	for _, u := range uses {
		if t, ok := tblmap[u.Table]; ok {
			addDep(t, dbstate.Ref{Kind: dbstate.KindSequence, Name: u.Sequence})
		}
	}

	owned := make(map[string][]*sequence, len(names))
	for _, s := range seqs {
		if !s.Identity && s.OwnerTable.Valid && s.OwnerColumn.Valid {
			owned[s.OwnerTable.String] = append(owned[s.OwnerTable.String], s)
			if t, ok := tblmap[s.OwnerTable.String]; ok {
				addDep(t, dbstate.Ref{Kind: dbstate.KindSequence, Name: s.Name})
			}
		}
	}

	tbls := make([]*dbstate.Object, 0, len(names))
	for _, n := range names {
		t := tblmap[n]
		t.Stmts = append(t.Stmts, createTable(n, colmap[n], conmap[n]))
		for _, s := range owned[n] {
			t.Stmts = append(t.Stmts, s.ownedBy())
		}
		t.Stmts = append(t.Stmts, idxmap[n]...)
		tbls = append(tbls, t)
	}
	return tbls, nil
}

func createTable(name string, cols []*column, cons []*constraint) string {
	defs := make([]string, 0, len(cols)+len(cons))
	for _, c := range cols {
		defs = append(defs, "    "+c.def())
	}
	for _, c := range cons {
		defs = append(defs, fmt.Sprintf("    CONSTRAINT %s %s", quoteIdent(c.Name), c.Def))
	}
	return fmt.Sprintf("CREATE TABLE %s (\n%s\n)", quoteIdent(name), strings.Join(defs, ",\n"))
}

// ---- records

type recordColumn struct {
	Name     string `db:"name"`
	Always   bool   `db:"always"`
	Category string `db:"category"`
	Bytea    bool   `db:"bytea"`
}

// A generated column cannot be given a value, so it is left out.
const qRecordColumns = `
SELECT a.attname AS name,
       a.attidentity = 'a' AS always,
       t.typcategory AS category,
       t.typname = 'bytea' AS bytea
  FROM pg_attribute a
  JOIN pg_type t ON t.oid = a.atttypid
 WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped
   AND a.attgenerated = ''
 ORDER BY a.attnum`

const qPrimaryKey = `
SELECT a.attname
  FROM pg_index i
  JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
 WHERE i.indrelid = to_regclass($1) AND i.indisprimary
 ORDER BY array_position(i.indkey::int2[], a.attnum)`

// literal forms by the type category
const (
	formQuoted = iota // already quoted by quote_nullable()
	formNumber
	formBytea
)

var reNumber = regexp.MustCompile(`^-?[0-9]`)

// getRecords reads all records of the table as SQL literals.
//
// The values are converted to text by the server, and only the form of the
// literal is decided by the type: numbers are bare, bytea is a hex literal,
// and the others are quoted by quote_nullable(), which applies the output
// function of the type (json, arrays and the like).
//
// Rows are ordered by the primary key, or by all values without it.
// The heap order of PostgreSQL is not stable (an UPDATE moves the row).
func getRecords(db *sqlx.DB, table string) (*dbstate.Records, error) {
	name := quoteIdent(table)

	var cols []recordColumn
	if err := db.Select(&cols, qRecordColumns, name); err != nil {
		return nil, err
	}

	rec := &dbstate.Records{
		Columns: make([]string, len(cols)),
	}
	qnames := make([]string, len(cols))
	exprs := make([]string, len(cols))
	forms := make([]int, len(cols))
	option := ""
	for i, c := range cols {
		rec.Columns[i] = c.Name
		qnames[i] = quoteIdent(c.Name)
		switch {
		case c.Bytea:
			forms[i] = formBytea
			exprs[i] = "encode(" + qnames[i] + ", 'hex')"
		case c.Category == "N":
			forms[i] = formNumber
			exprs[i] = qnames[i] + "::text"
		default:
			forms[i] = formQuoted
			exprs[i] = "quote_nullable(" + qnames[i] + ")"
		}
		if c.Always {
			option = "OVERRIDING SYSTEM VALUE "
		}
	}
	rec.Insert = fmt.Sprintf("INSERT INTO %s (%s) %sVALUES", name, strings.Join(qnames, ", "), option)
	if len(cols) == 0 {
		return rec, nil
	}

	var pk []string
	if err := db.Select(&pk, qPrimaryKey, name); err != nil {
		return nil, err
	}
	for i := range pk {
		pk[i] = quoteIdent(pk[i])
	}

	q := fmt.Sprintf("SELECT %s FROM %s", strings.Join(exprs, ", "), name)
	if len(pk) > 0 {
		q += " ORDER BY " + strings.Join(pk, ", ")
	}

	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(dbstate.Row, len(cols))
		for i, v := range vals {
			row[i] = literal(forms[i], v)
		}
		rec.Rows = append(rec.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(pk) == 0 {
		slices.SortFunc(rec.Rows, func(a, b dbstate.Row) int { return slices.Compare(a, b) })
	}
	return rec, nil
}

func literal(form int, v sql.NullString) string {
	if !v.Valid {
		return "NULL"
	}
	switch form {
	case formNumber:
		if !reNumber.MatchString(v.String) {
			return quoteLiteral(v.String) // NaN, Infinity, money
		}
	case formBytea:
		return `'\x` + v.String + `'`
	}
	return v.String
}
