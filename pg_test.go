package sqlmigration

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/makiuchi-d/sql-migration/dbstate"
	"github.com/makiuchi-d/sql-migration/dialect/pg"
	"github.com/makiuchi-d/sql-migration/migrations"
)

var pgd = pg.New()

func TestCheckPG(t *testing.T) {
	dir := filepath.Join("testdata", "pg", "check")
	tests := map[string]struct {
		dir  string
		num  int
		diff string
	}{
		"success-create": {"success", 10, ""},
		"success-insert": {"success", 20, ""},
		"diff-counter": {
			"counter", 20,
			"" +
				"sequence \"users_id_seq\" differs:\n" +
				" CREATE SEQUENCE \"users_id_seq\" AS integer START WITH 1 INCREMENT BY 1 MINVALUE 1 MAXVALUE 2147483647 CACHE 1\n" +
				"-SELECT setval('\"users_id_seq\"', 1, false)\n" +
				"+SELECT setval('\"users_id_seq\"', 2, true)",
		},
		"diff-record": {
			"record", 20,
			"" +
				"records in \"tags\" differs:\n" +
				"+('a', NULL)",
		},
		"diff-table": {
			"table", 20,
			`unexpected table "extra" found`,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			diff, err := Check(io.Discard, filepath.Join(dir, test.dir), test.num, pgd)
			if err != nil {
				t.Fatal(err)
			}
			if d := cmp.Diff(test.diff, diff); d != "" {
				t.Error(d)
			}
		})
	}
}

func TestCheckPGSnapshotDiffers(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join("testdata", "pg", "check", "success")
	copyFiles(t, src, dir,
		"000000_init.all.sql",
		"000010_create.up.sql", "000010_create.down.sql",
		"000020_insert.up.sql", "000020_insert.down.sql",
	)
	all, err := os.ReadFile(filepath.Join(src, "000020_insert.all.sql"))
	if err != nil {
		t.Fatal(err)
	}
	all = bytes.Replace(all, []byte("('b', 2)"), []byte("('b', 3)"), 1)
	if err := os.WriteFile(filepath.Join(dir, "000020_insert.all.sql"), all, 0666); err != nil {
		t.Fatal(err)
	}

	diff, err := Check(io.Discard, dir, 20, pgd)
	if err != nil {
		t.Fatal(err)
	}
	exp := "" +
		"records in \"tags\" differs:\n" +
		"...\n" +
		" ('a', NULL)\n" +
		"-('b', 2)\n" +
		"+('b', 3)"
	if d := cmp.Diff(exp, diff); d != "" {
		t.Error(d)
	}
}

func TestSnapshotPG(t *testing.T) {
	dir := t.TempDir()
	copyFiles(t, filepath.Join("testdata", "pg", "check", "success"), dir,
		"000000_init.all.sql",
		"000010_create.up.sql", "000010_create.down.sql",
		"000020_insert.up.sql", "000020_insert.down.sql",
	)

	// expected state
	db, err := pgd.OpenTemp()
	if err != nil {
		t.Fatal(err)
	}
	if err := applyFiles(io.Discard, db, pgd, dir,
		"000000_init.all.sql", "000010_create.up.sql", "000020_insert.up.sql"); err != nil {
		t.Fatal(err)
	}
	exp, err := pgd.TakeSnapshot(db)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	var buf bytes.Buffer
	if err := Snapshot(io.Discard, &buf, dir, -1, "pigy", pgd); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "000020_insert.all.sql")
	if err := os.WriteFile(path, buf.Bytes(), 0666); err != nil {
		t.Fatal(err)
	}

	db, err = OpenDumpFile(pgd, path)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := pgd.TakeSnapshot(db)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if diff := dbstate.Diff(exp, ss, dbstate.DiffOptions{Ignores: snapshotIgnores}); diff != "" {
		t.Fatal("diff snapshot:\n" + diff)
	}

	// the same state always gives the same file
	var buf2 bytes.Buffer
	if err := Snapshot(io.Discard, &buf2, dir, -1, "pigy", pgd); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(withoutApplied(buf.Bytes()), withoutApplied(buf2.Bytes())) {
		t.Errorf("not deterministic:\n%s\n----\n%s", buf.Bytes(), buf2.Bytes())
	}
}

func TestApplyPG(t *testing.T) {
	dir := filepath.Join("testdata", "pg", "check", "success")
	db, err := pgd.OpenTemp()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	files, err := ListFiles(db, dir, -1, pgd)
	if err != nil {
		t.Fatal(err)
	}
	// starts from the latest *.all.sql
	if d := cmp.Diff([]string{"000020_insert.all.sql"}, files); d != "" {
		t.Error(d)
	}

	tests := []struct {
		name   string
		num    int
		result int
	}{
		{"empty->10", 10, 10},
		{"10->20(latest)", -1, 20},
		{"20->0", 0, 0},
		{"0->0 (nothing to do)", 0, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := Apply(io.Discard, db, dir, test.num, pgd); err != nil {
				t.Fatal(err)
			}
			hs, err := migrations.LoadHistories(db)
			if err != nil {
				t.Fatal(err)
			}
			if num := hs.CurrentNum(); num != test.result {
				t.Fatalf("current num = %v, wants %v", num, test.result)
			}
		})
	}

	st, err := Status(db, dir, pgd)
	if err != nil {
		t.Fatal(err)
	}
	if exp := "000010\t⏫⏬　\t　0000-00-00 --:--:--\t\"create\"\n"; !bytes.Contains([]byte(st), []byte(exp)) {
		t.Errorf("status:\n%s", st)
	}

	// the down template fails when the migration is not applied
	err = applyFile(db, pgd, filepath.Join(dir, "000010_create.down.sql"))
	if err == nil {
		t.Error("down.sql of a migration not applied must fail")
	}
}
