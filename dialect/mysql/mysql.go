// Package mysql is the MySQL dialect.
//
// The temporary database is go-mysql-server running in the process.
package mysql

import (
	"fmt"
	"iter"
	"sync/atomic"

	"github.com/jmoiron/sqlx"
	"github.com/makiuchi-d/testdb"

	"github.com/makiuchi-d/sql-migration/dbstate"
	"github.com/makiuchi-d/sql-migration/dialect"
)

// Dialect is the MySQL dialect.
type Dialect struct{}

var _ dialect.Dialect = Dialect{}

const initSQL = `CREATE TABLE _migrations (
   id      INTEGER NOT NULL,
   applied DATETIME,
   title   VARCHAR(255),
   PRIMARY KEY (id)
);

INSERT INTO _migrations (id, applied, title) VALUES (0, now(), 'init');

DELIMITER //

CREATE PROCEDURE _migration_exists(IN input_id INTEGER)
BEGIN
  IF NOT EXISTS (SELECT 1 FROM _migrations WHERE id = input_id) THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'migration not found';
  END IF;
END//

DELIMITER ;
`

const upTemplate = `INSERT INTO _migrations (id, title, applied) VALUES ({{.Number}}, '{{.Title}}', now());
-- Write your forward migration SQL statements below.
`

const downTemplate = `CALL _migration_exists({{.Number}});
DELETE FROM _migrations WHERE id = {{.Number}};
-- Write your rollback SQL statements below.
`

func (Dialect) InitSQL() string      { return initSQL }
func (Dialect) UpTemplate() string   { return upTemplate }
func (Dialect) DownTemplate() string { return downTemplate }

var tempCount atomic.Int64

// OpenTemp returns a new in-process database.
func (Dialect) OpenTemp() (*sqlx.DB, error) {
	name := fmt.Sprintf("tmp%d", tempCount.Add(1))
	return sqlx.NewDb(testdb.New(name), "mysql"), nil
}

func (Dialect) Statements(input []byte) iter.Seq[string] {
	return Statements(input)
}

func (Dialect) TakeSnapshot(db *sqlx.DB) (*dbstate.Snapshot, error) {
	return TakeSnapshot(db)
}

func (Dialect) CheckMigrationTable(db *sqlx.DB) error {
	return CheckMigrationTable(db)
}
