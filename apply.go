package sqlmigration

import (
	"database/sql"
	"errors"
	"fmt"
	"io"

	"github.com/jmoiron/sqlx"

	"github.com/makiuchi-d/sql-migration/dialect"
	"github.com/makiuchi-d/sql-migration/migrations"
)

// ListFiles returns the migration files to move the database to num
// (latest if num < 0).
// A database without '_migrations' table is regarded as empty: the files
// start from the latest *.all.sql.
func ListFiles(db *sqlx.DB, dir string, num int, d dialect.Dialect) ([]string, error) {
	migs, err := migrations.Load(dir)
	if err != nil {
		return nil, err
	}
	if num < 0 {
		num = migs.Last().Number
	}

	err = d.CheckMigrationTable(db)
	if err != nil {
		if !errors.Is(err, dialect.ErrNoMigrationTable) {
			return nil, err
		}
		i, err := migs.FindNumber(num)
		if err != nil {
			return nil, err
		}
		return migs[:i+1].FileNamesFromSnapshot()
	}

	hists, err := migrations.LoadHistories(db)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("'_migrations' table found but not initialized")
		}
		return nil, err
	}
	ms := make(migrations.Migrations, 0, len(migs))
	for s := range migrations.BuildStatus(migs, hists) {
		ms = append(ms, s.Migration)
	}
	return ms.FileNamesToApply(hists.CurrentNum(), num)
}

// Apply applies the migration files to move the database to num
// (latest if num < 0).
func Apply(w io.Writer, db *sqlx.DB, dir string, num int, d dialect.Dialect) error {
	files, err := ListFiles(db, dir, num, d)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		info(w, "Nothing to do.")
		return nil
	}
	return applyFiles(w, db, d, dir, files...)
}

// Status returns the status of each migration.
// db can be nil, then only the files are listed.
func Status(db *sqlx.DB, dir string, d dialect.Dialect) (string, error) {
	var hists []migrations.History
	if db != nil {
		err := d.CheckMigrationTable(db)
		if err == nil {
			hists, err = migrations.LoadHistories(db)
		}
		if err != nil && !errors.Is(err, dialect.ErrNoMigrationTable) {
			return "", err
		}
	}

	migs, err := migrations.Load(dir)
	if err != nil {
		return "", err
	}

	var b []byte
	for st := range migrations.BuildStatus(migs, hists) {
		b = append(formatStatus(b, st), '\n')
	}
	return string(b), nil
}

func formatStatus(b []byte, st migrations.Status) []byte {
	b = fmt.Appendf(b, "%06d\t", st.Number)
	if st.UpDown {
		b = fmt.Append(b, "⏫⏬")
	} else {
		b = fmt.Append(b, "　　")
	}
	if st.Snapshot {
		b = fmt.Append(b, "⏺\t")
	} else {
		b = fmt.Append(b, "　\t")
	}
	if st.IsApplied() {
		b = st.Applied.AppendFormat(b, "✅2006-01-02 15:04:05\t")
	} else {
		b = fmt.Append(b, "　0000-00-00 --:--:--\t")
	}
	if st.DBTitle == "" {
		b = fmt.Appendf(b, "%q", st.Title)
	} else {
		b = fmt.Appendf(b, "⚠%q DB:%q", st.Title, st.DBTitle)
	}
	return b
}
