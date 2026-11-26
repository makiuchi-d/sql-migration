// Package dbstate holds the state of a database schema in a dialect neutral
// container, and compares and writes it out.
//
// All dialect knowledge is baked into strings when a Snapshot is taken:
// Diff and Dump never know which database the Snapshot came from.
package dbstate

import (
	"strings"
)

// Snapshot is the whole schema state at a migration point.
//
// Objects is sorted in a creatable order (dependencies first).
// It is an invariant kept by whoever builds the Snapshot.
type Snapshot struct {
	Objects []*Object
	Records map[string]*Records // key: the Name of an Object of KindTable
}

// Kind is the kind of a schema object.
type Kind string

const (
	KindTable     Kind = "table"
	KindSequence  Kind = "sequence"
	KindView      Kind = "view"
	KindProcedure Kind = "procedure"
	KindFunction  Kind = "function"
	KindTrigger   Kind = "trigger"
	KindType      Kind = "type"
)

// AfterRecords reports whether the objects of the kind must be written after
// all records. A trigger created before the records would fire on restoring.
func (k Kind) AfterRecords() bool {
	return k == KindTrigger
}

// Object is a schema object.
type Object struct {
	Kind  Kind
	Name  string   // raw identifier (not quoted)
	Deps  []Ref    // objects that must exist before this one
	Stmts []string // executable statements without terminators

	// Text representation in a SQL file. The zero values are the common form:
	//   Before + Stmts[0] + Term + "\n" + Stmts[1] + Term ... + After
	Term   string // statement terminator in a file; "" means ";"
	Before string // e.g. "DELIMITER //\n"
	After  string // e.g. "\nDELIMITER ;"
}

// Ref refers an Object.
type Ref struct {
	Kind Kind
	Name string
}

// Ref returns the reference to the object itself.
func (o *Object) Ref() Ref {
	return Ref{o.Kind, o.Name}
}

// Text returns the object as it is written in a SQL file.
func (o *Object) Text() string {
	term := o.Term
	if term == "" {
		term = ";"
	}
	var b strings.Builder
	b.WriteString(o.Before)
	for i, s := range o.Stmts {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s)
		b.WriteString(term)
	}
	b.WriteString(o.After)
	return b.String()
}

// Records holds all rows of a table.
type Records struct {
	Columns []string // raw column names, used to match ignores
	Rows    []Row

	// Insert is the head of an INSERT statement written in a SQL file,
	// up to VALUES. e.g. `INSERT INTO "t" ("a", "b") VALUES`
	Insert string
}

// Row holds the values of a record as quoted SQL literals.
// A NULL is the bare word NULL.
type Row []string

func (r Row) String() string {
	return "(" + strings.Join(r, ", ") + ")"
}

// Inserts returns INSERT statements in a SQL file, each holds up to chunk rows.
func (r *Records) Inserts(chunk int) []string {
	var stmts []string
	for i := 0; i < len(r.Rows); i += chunk {
		rows := r.Rows[i:min(i+chunk, len(r.Rows))]
		var b strings.Builder
		b.WriteString(r.Insert)
		for j, row := range rows {
			if j > 0 {
				b.WriteByte(',')
			}
			b.WriteString("\n  ")
			b.WriteString(row.String())
		}
		b.WriteByte(';')
		stmts = append(stmts, b.String())
	}
	return stmts
}
