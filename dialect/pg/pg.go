// Package pg is the PostgreSQL dialect.
//
// The temporary database is pgmem, a PostgreSQL server running in the process.
package pg

import (
	"context"
	"iter"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/shibukawa/pgmem"

	"github.com/makiuchi-d/sql-migration/dbstate"
	"github.com/makiuchi-d/sql-migration/dialect"
)

// Dialect is the PostgreSQL dialect.
// It starts the in-process server once, and empties it on each OpenTemp.
type Dialect struct {
	once   sync.Once
	server *pgmem.Server
	err    error
}

var _ dialect.Dialect = (*Dialect)(nil)

// New returns a PostgreSQL dialect.
func New() *Dialect {
	return &Dialect{}
}

const initSQL = `CREATE TABLE _migrations (
   id      INTEGER NOT NULL,
   applied TIMESTAMPTZ,
   title   VARCHAR(255),
   PRIMARY KEY (id)
);

INSERT INTO _migrations (id, applied, title) VALUES (0, now(), 'init');
`

const upTemplate = `INSERT INTO _migrations (id, title, applied) VALUES ({{.Number}}, '{{.Title}}', now());
-- Write your forward migration SQL statements below.
`

// The division by zero fails the migration if the record does not exist.
const downTemplate = `WITH deleted AS (DELETE FROM _migrations WHERE id = {{.Number}} RETURNING id) SELECT 1/count(*) FROM deleted;
-- Write your rollback SQL statements below.
`

func (*Dialect) InitSQL() string      { return initSQL }
func (*Dialect) UpTemplate() string   { return upTemplate }
func (*Dialect) DownTemplate() string { return downTemplate }

// OpenTemp returns an empty database of the in-process server.
// The server is started on the first call; later calls empty the same
// database, so the databases returned before are emptied too.
func (d *Dialect) OpenTemp() (*sqlx.DB, error) {
	d.once.Do(func() {
		d.server, d.err = pgmem.Start(context.Background(), pgmem.Options{})
	})
	if d.err != nil {
		return nil, d.err
	}
	cfg, err := pgx.ParseConfig(d.server.DSN())
	if err != nil {
		return nil, err
	}
	cfg.DialFunc = d.server.Dial
	db := sqlx.NewDb(stdlib.OpenDB(*cfg), "pgx")

	for _, q := range []string{
		"DROP SCHEMA IF EXISTS public CASCADE",
		"CREATE SCHEMA public",
	} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// Close stops the in-process server if started.
func (d *Dialect) Close() error {
	if d.server == nil {
		return nil
	}
	return d.server.Close()
}

func (*Dialect) Statements(input []byte) iter.Seq[string] {
	return Statements(input)
}

func (*Dialect) TakeSnapshot(db *sqlx.DB) (*dbstate.Snapshot, error) {
	return TakeSnapshot(db)
}

func (*Dialect) CheckMigrationTable(db *sqlx.DB) error {
	return CheckMigrationTable(db)
}
