package sqlmigration

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/makiuchi-d/sql-migration/dbstate"
	"github.com/makiuchi-d/sql-migration/dialect"
	"github.com/makiuchi-d/sql-migration/migrations"
)

// snapshotIgnores are the built-in ignores to verify a *.all.sql.
var snapshotIgnores = map[string][]string{"_migrations": {"applied"}}

// target loads the migrations up to num (latest if num < 0) and returns them
// with the files to restore the state just before the last one.
func target(dir string, num int) (*migrations.Migration, []string, error) {
	migs, err := migrations.Load(dir)
	if err != nil {
		return nil, nil, err
	}
	if num >= 0 {
		i, err := migs.FindNumber(num)
		if err != nil {
			return nil, nil, err
		}
		migs = migs[:i+1]
	}
	if len(migs) < 2 {
		return nil, nil, fmt.Errorf("no migration to process")
	}
	mig := migs.Last()
	if !mig.UpDown {
		return nil, nil, fmt.Errorf("no up/down migration: number=%06d", mig.Number)
	}
	files, err := migs[:len(migs)-1].FileNamesFromSnapshot()
	if err != nil {
		return nil, nil, err
	}
	return mig, files, nil
}

// Check checks that the up/down migration of num (latest if num < 0) is
// reversible, and that its *.all.sql, if exists, reproduces the state after
// the up migration.
// It returns the differences found, or "" if none.
func Check(w io.Writer, dir string, num int, d dialect.Dialect) (string, error) {
	mig, files, err := target(dir, num)
	if err != nil {
		return "", err
	}

	db, err := d.OpenTemp()
	if err != nil {
		return "", err
	}
	defer db.Close()

	if err := applyFiles(w, db, d, dir, files...); err != nil {
		return "", err
	}
	ss1, err := d.TakeSnapshot(db)
	if err != nil {
		return "", err
	}

	info(w, "---- up/down")
	if err := applyFiles(w, db, d, dir, mig.UpName()); err != nil {
		return "", err
	}
	var ss2 *dbstate.Snapshot
	if mig.Snapshot {
		ss2, err = d.TakeSnapshot(db)
		if err != nil {
			return "", err
		}
	}
	if err := applyFiles(w, db, d, dir, mig.DownName()); err != nil {
		return "", err
	}
	ss3, err := d.TakeSnapshot(db)
	if err != nil {
		return "", err
	}
	info(w, "checking...")
	if diff := dbstate.Diff(ss1, ss3, dbstate.DiffOptions{Ignores: mig.Ignores}); diff != "" {
		return strings.TrimSuffix(diff, "\n"), nil
	}
	info(w, "ok")

	if !mig.Snapshot {
		return "", nil
	}

	info(w, "---- snapshot")
	db.Close()
	db2, err := d.OpenTemp()
	if err != nil {
		return "", err
	}
	defer db2.Close()
	if err := applyFiles(w, db2, d, dir, mig.SnapshotName()); err != nil {
		return "", err
	}
	ss4, err := d.TakeSnapshot(db2)
	if err != nil {
		return "", err
	}
	info(w, "checking...")
	if diff := dbstate.Diff(ss2, ss4, dbstate.DiffOptions{Ignores: snapshotIgnores}); diff != "" {
		return strings.TrimSuffix(diff, "\n"), nil
	}
	info(w, "ok")
	return "", nil
}

// SnapshotFile returns the path of the *.all.sql that Snapshot makes for num
// (latest if num < 0).
func SnapshotFile(dir string, num int) (string, error) {
	mig, _, err := target(dir, num)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, mig.SnapshotName()), nil
}

// Snapshot applies the migrations up to num (latest if num < 0) and writes
// the state as a SQL file to out.
// name is the tool name written in the signature line.
func Snapshot(w, out io.Writer, dir string, num int, name string, d dialect.Dialect) error {
	mig, files, err := target(dir, num)
	if err != nil {
		return err
	}

	db, err := d.OpenTemp()
	if err != nil {
		return err
	}
	defer db.Close()

	if err := applyFiles(w, db, d, dir, append(files, mig.UpName())...); err != nil {
		return err
	}
	ss, err := d.TakeSnapshot(db)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprint(out, signature(name), "\n\n"); err != nil {
		return err
	}
	return dbstate.Dump(out, ss)
}
