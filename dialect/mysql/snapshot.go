package mysql

import (
	"cmp"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/makiuchi-d/sql-migration/dbstate"
	"github.com/makiuchi-d/sql-migration/dialect"
)

// quoteIdent quotes an identifier with backquotes.
func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// quoteString quotes a string as a MySQL string literal.
func quoteString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for _, c := range s {
		switch c {
		case 0:
			b.WriteString(`\0`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case 26: // ^Z
			b.WriteString(`\Z`)
		case '\'':
			b.WriteString(`\'`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

func CheckMigrationTable(db *sqlx.DB) error {
	const q = `SELECT COUNT(*) FROM information_schema.tables
 WHERE table_schema = DATABASE() AND table_name = '_migrations'`
	var n int
	if err := db.Get(&n, q); err != nil {
		return err
	}
	if n == 0 {
		return dialect.ErrNoMigrationTable
	}
	return nil
}

// TakeSnapshot reads the tables, their records and the stored procedures.
func TakeSnapshot(db *sqlx.DB) (*dbstate.Snapshot, error) {
	ss := &dbstate.Snapshot{
		Records: make(map[string]*dbstate.Records),
	}

	tbls, err := getTables(db)
	if err != nil {
		return nil, err
	}
	for _, t := range tbls {
		ss.Objects = append(ss.Objects, t.obj)
		rec, err := getRecords(db, t)
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", t.obj.Name, err)
		}
		ss.Records[t.obj.Name] = rec
	}

	procs, err := getProcedures(db)
	if err != nil {
		return nil, err
	}
	// The tables a procedure uses are not known without parsing its body,
	// but go-mysql-server requires them on CREATE PROCEDURE.
	// So procedures are created after all tables.
	for _, p := range procs {
		for _, t := range tbls {
			p.Deps = append(p.Deps, t.obj.Ref())
		}
	}
	ss.Objects = append(ss.Objects, procs...)

	ss.Objects, err = dbstate.SortObjects(ss.Objects)
	if err != nil {
		return nil, err
	}
	return ss, nil
}

type table struct {
	obj       *dbstate.Object
	generated []string // generated columns
}

var (
	reRef           = regexp.MustCompile("REFERENCES `((?:[^`]|``)+)`")
	reAutoIncrement = regexp.MustCompile(` AUTO_INCREMENT=([0-9]+)`)
	reGenerated     = regexp.MustCompile("(?m)^\\s*`((?:[^`]|``)+)` .* GENERATED ALWAYS AS ")
)

func unquoteIdent(s string) string {
	return strings.ReplaceAll(s, "``", "`")
}

func getTables(db *sqlx.DB) ([]*table, error) {
	const q = `SELECT table_name FROM information_schema.tables
 WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE'
 ORDER BY table_name`
	var names []string
	if err := db.Select(&names, q); err != nil {
		return nil, err
	}

	tbls := make([]*table, 0, len(names))
	for _, n := range names {
		var ct struct {
			Table  string `db:"Table"`
			Create string `db:"Create Table"`
		}
		if err := db.Get(&ct, "SHOW CREATE TABLE "+quoteIdent(n)); err != nil {
			return nil, err
		}
		tbls = append(tbls, buildTable(n, ct.Create))
	}
	return tbls, nil
}

// buildTable makes a table object from the text of SHOW CREATE TABLE.
// The AUTO_INCREMENT counter is taken out of the CREATE TABLE as an
// independent statement, so that a change of the counter is not mixed up with
// a change of the definition.
func buildTable(name, create string) *table {
	stmts := []string{create}

	// table options are in the last line
	if i := strings.LastIndexByte(create, '\n'); i >= 0 {
		opts := create[i:]
		if m := reAutoIncrement.FindStringSubmatchIndex(opts); m != nil {
			stmts[0] = create[:i] + opts[:m[0]] + opts[m[1]:]
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s AUTO_INCREMENT = %s",
				quoteIdent(name), opts[m[2]:m[3]]))
		}
	}

	t := &table{
		obj: &dbstate.Object{
			Kind:  dbstate.KindTable,
			Name:  name,
			Stmts: stmts,
		},
	}
	for _, m := range reRef.FindAllStringSubmatch(create, -1) {
		ref := dbstate.Ref{Kind: dbstate.KindTable, Name: unquoteIdent(m[1])}
		if ref.Name != name && !slices.Contains(t.obj.Deps, ref) {
			t.obj.Deps = append(t.obj.Deps, ref)
		}
	}
	for _, m := range reGenerated.FindAllStringSubmatch(create, -1) {
		t.generated = append(t.generated, unquoteIdent(m[1]))
	}
	return t
}

// knownProcedures are the procedures read even if the catalog does not list them.
// go-mysql-server does not have a catalog of the stored procedures
// (information_schema.routines is empty).
var knownProcedures = []string{"_migration_exists"}

func getProcedures(db *sqlx.DB) ([]*dbstate.Object, error) {
	const q = `SELECT routine_name FROM information_schema.routines
 WHERE routine_schema = DATABASE() AND routine_type = 'PROCEDURE'`
	var names []string
	if err := db.Select(&names, q); err != nil {
		return nil, err
	}
	listed := len(names)
	for _, n := range knownProcedures {
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}

	objs := make([]*dbstate.Object, 0, len(names))
	for i, n := range names {
		var p struct {
			Create string `db:"Create Procedure"`
		}
		err := db.Unsafe().Get(&p, "SHOW CREATE PROCEDURE "+quoteIdent(n))
		if err != nil {
			if i >= listed {
				// a known procedure which does not exist
				continue
			}
			return nil, err
		}
		objs = append(objs, &dbstate.Object{
			Kind:   dbstate.KindProcedure,
			Name:   n,
			Stmts:  []string{p.Create},
			Term:   "//",
			Before: "DELIMITER //\n",
			After:  "\nDELIMITER ;",
		})
	}
	return objs, nil
}

type column struct {
	Name     string `db:"name"`
	DataType string `db:"data_type"`
}

// literal forms by the data type
const (
	formString = iota
	formNumber
	formBinary
)

func literalForm(dataType string) int {
	switch strings.ToLower(dataType) {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint",
		"decimal", "numeric", "float", "double", "real", "year":
		return formNumber
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bit":
		return formBinary
	}
	return formString
}

// getRecords reads all records of the table as SQL literals.
//
// The values are converted to text by the server (CAST AS CHAR, HEX), and only
// the form of the literal is decided by the data type of the column.
// Rows are ordered by the primary key, or by all values without it.
func getRecords(db *sqlx.DB, t *table) (*dbstate.Records, error) {
	name := t.obj.Name

	const qcols = `SELECT column_name AS name, data_type AS data_type FROM information_schema.columns
 WHERE table_schema = DATABASE() AND table_name = ?
 ORDER BY ordinal_position`
	var allcols []column
	if err := db.Select(&allcols, qcols, name); err != nil {
		return nil, err
	}
	var cols []column
	for _, c := range allcols {
		if !slices.Contains(t.generated, c.Name) {
			cols = append(cols, c)
		}
	}

	rec := &dbstate.Records{
		Columns: make([]string, len(cols)),
	}
	qnames := make([]string, len(cols))
	exprs := make([]string, len(cols))
	forms := make([]int, len(cols))
	for i, c := range cols {
		rec.Columns[i] = c.Name
		qnames[i] = quoteIdent(c.Name)
		forms[i] = literalForm(c.DataType)
		if forms[i] == formBinary {
			exprs[i] = "HEX(" + qnames[i] + ")"
		} else {
			exprs[i] = "CAST(" + qnames[i] + " AS CHAR)"
		}
	}
	rec.Insert = fmt.Sprintf("INSERT INTO %s (%s) VALUES",
		quoteIdent(name), strings.Join(qnames, ", "))
	if len(cols) == 0 {
		return rec, nil
	}

	pk, err := primaryKey(db, name)
	if err != nil {
		return nil, err
	}

	q := fmt.Sprintf("SELECT %s FROM %s", strings.Join(exprs, ", "), quoteIdent(name))
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
		return v.String
	case formBinary:
		h := v.String
		if len(h)%2 != 0 {
			h = "0" + h
		}
		return "X'" + h + "'"
	}
	return quoteString(v.String)
}

type keyColumn struct {
	Column string `db:"Column_name"`
	Seq    int    `db:"Seq_in_index"`
}

func primaryKey(db *sqlx.DB, table string) ([]string, error) {
	var keys []keyColumn
	q := "SHOW KEYS FROM " + quoteIdent(table) + " WHERE Key_name = 'PRIMARY'"
	if err := db.Unsafe().Select(&keys, q); err != nil {
		return nil, err
	}
	slices.SortFunc(keys, func(a, b keyColumn) int { return cmp.Compare(a.Seq, b.Seq) })
	pk := make([]string, len(keys))
	for i, k := range keys {
		pk[i] = quoteIdent(k.Column)
	}
	return pk, nil
}
