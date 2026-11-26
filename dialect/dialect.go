// Package dialect defines what a SQL dialect provides.
package dialect

import (
	"errors"
	"iter"

	"github.com/jmoiron/sqlx"

	"github.com/makiuchi-d/sql-migration/dbstate"
)

// ErrNoMigrationTable is returned by CheckMigrationTable when the database has
// no '_migrations' table.
var ErrNoMigrationTable = errors.New("no '_migrations' table")

// Dialect is the knowledge of a database engine.
type Dialect interface {
	// InitSQL returns the body of the initial migration file (000000_init.all.sql).
	InitSQL() string
	// UpTemplate returns the text/template of a new up.sql (.Number, .Title).
	UpTemplate() string
	// DownTemplate returns the text/template of a new down.sql (.Number, .Title).
	DownTemplate() string

	// OpenTemp returns an empty temporary database.
	// A database returned before may become invalid.
	OpenTemp() (*sqlx.DB, error)
	// Statements splits a text of multiple statements into each statement.
	Statements([]byte) iter.Seq[string]
	// TakeSnapshot reads the state of the database.
	TakeSnapshot(*sqlx.DB) (*dbstate.Snapshot, error)
	// CheckMigrationTable returns ErrNoMigrationTable if the database has no
	// '_migrations' table.
	CheckMigrationTable(*sqlx.DB) error
}
