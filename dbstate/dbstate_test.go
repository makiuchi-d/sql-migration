package dbstate

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func table(name string, stmts ...string) *Object {
	return &Object{Kind: KindTable, Name: name, Stmts: stmts}
}

func TestDiff(t *testing.T) {
	base := func() *Snapshot {
		return &Snapshot{
			Objects: []*Object{
				{Kind: KindSequence, Name: "s", Stmts: []string{"CREATE SEQUENCE s", "SELECT setval('s', 1)"}},
				table("t1", "CREATE TABLE t1 (\n  a int,\n  b int,\n  c int,\n  d int,\n  e int,\n  f int\n)"),
				table("t2", "CREATE TABLE t2 (id int, v text)"),
			},
			Records: map[string]*Records{
				"t1": {Columns: []string{"a", "b", "c", "d", "e", "f"}},
				"t2": {Columns: []string{"id", "v"}, Rows: []Row{{"1", "'a'"}, {"2", "'b'"}, {"3", "'c'"}}},
			},
		}
	}

	tests := map[string]struct {
		modify func(ss *Snapshot)
		opt    DiffOptions
		exp    string
	}{
		"same": {
			modify: func(ss *Snapshot) {},
		},
		"order is not compared": {
			modify: func(ss *Snapshot) {
				ss.Objects[0], ss.Objects[2] = ss.Objects[2], ss.Objects[0]
				ss.Objects[1].Deps = []Ref{{KindSequence, "s"}}
				ss.Objects[1].Term = "//"
			},
		},
		"unexpected": {
			modify: func(ss *Snapshot) {
				ss.Objects = append(ss.Objects, &Object{Kind: KindView, Name: "v"})
			},
			exp: "unexpected view \"v\" found\n",
		},
		"missing": {
			modify: func(ss *Snapshot) {
				ss.Objects = ss.Objects[1:]
			},
			exp: "missing sequence \"s\"\n",
		},
		"same name, different kind": {
			modify: func(ss *Snapshot) {
				ss.Objects[0].Kind = KindTable
			},
			exp: "unexpected table \"s\" found\nmissing sequence \"s\"\n",
		},
		"stmts differ": {
			modify: func(ss *Snapshot) {
				ss.Objects[1].Stmts[0] = strings.Replace(ss.Objects[1].Stmts[0], "f int", "f text", 1)
				ss.Objects[1].Stmts = append(ss.Objects[1].Stmts, "ALTER TABLE t1 AUTO_INCREMENT = 2")
				// records are not compared when the definition differs
				ss.Records["t1"].Rows = []Row{{"1", "2", "3", "4", "5", "6"}}
			},
			exp: "" +
				"table \"t1\" differs:\n" +
				"...\n" +
				"   e int,\n" +
				"-  f int\n" +
				"+  f text\n" +
				" )\n" +
				"+ALTER TABLE t1 AUTO_INCREMENT = 2\n",
		},
		"records differ": {
			modify: func(ss *Snapshot) {
				ss.Records["t2"].Rows[1] = Row{"2", "'B'"}
			},
			exp: "" +
				"records in \"t2\" differs:\n" +
				" (1, 'a')\n" +
				"-(2, 'b')\n" +
				"+(2, 'B')\n" +
				" (3, 'c')\n",
		},
		"ignored column": {
			modify: func(ss *Snapshot) {
				ss.Records["t2"].Rows[1] = Row{"2", "'B'"}
			},
			opt: DiffOptions{Ignores: map[string][]string{"t2": {"v"}}},
		},
		"ignored table": {
			modify: func(ss *Snapshot) {
				ss.Records["t2"].Rows = nil
			},
			opt: DiffOptions{Ignores: map[string][]string{"t2": {"*"}}},
		},
		"ignore is exact match": {
			modify: func(ss *Snapshot) {
				ss.Records["t2"].Rows[1] = Row{"2", "'B'"}
			},
			opt: DiffOptions{Ignores: map[string][]string{"t2": {"V"}, "T2": {"v"}}},
			exp: "" +
				"records in \"t2\" differs:\n" +
				" (1, 'a')\n" +
				"-(2, 'b')\n" +
				"+(2, 'B')\n" +
				" (3, 'c')\n",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			after := base()
			test.modify(after)
			diff := Diff(base(), after, test.opt)
			if d := cmp.Diff(test.exp, diff); d != "" {
				t.Error(d)
			}
		})
	}
}

func TestSortObjects(t *testing.T) {
	objs := []*Object{
		{Kind: KindTable, Name: "a", Deps: []Ref{{KindTable, "c"}, {KindTable, "a"}}},
		{Kind: KindTable, Name: "b"},
		{Kind: KindSequence, Name: "z_seq", Deps: []Ref{{KindTable, "c"}}},
		{Kind: KindTable, Name: "c", Deps: []Ref{{KindSequence, "c_seq"}, {KindTable, "other_schema"}}},
		{Kind: KindSequence, Name: "c_seq"},
	}
	sorted, err := SortObjects(objs)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range sorted {
		names = append(names, o.Name)
	}
	exp := []string{"c_seq", "b", "c", "z_seq", "a"}
	if d := cmp.Diff(exp, names); d != "" {
		t.Error(d)
	}

	// circular
	objs = []*Object{
		{Kind: KindTable, Name: "a", Deps: []Ref{{KindTable, "b"}}},
		{Kind: KindTable, Name: "b", Deps: []Ref{{KindTable, "a"}}},
		{Kind: KindTable, Name: "c"},
	}
	_, err = SortObjects(objs)
	if err == nil || !strings.Contains(err.Error(), `table "a", table "b"`) {
		t.Errorf("circular dependency error wanted: %v", err)
	}
}

func TestDump(t *testing.T) {
	rows := make([]Row, 12)
	for i := range rows {
		rows[i] = Row{string(rune('a' + i)), "NULL"}
	}
	ss := &Snapshot{
		Objects: []*Object{
			{Kind: KindTrigger, Name: "trg", Stmts: []string{"CREATE TRIGGER trg"}},
			{Kind: KindSequence, Name: "seq", Stmts: []string{"CREATE SEQUENCE seq", "SELECT setval('seq', 3)"}},
			table("t", "CREATE TABLE t (a int, b int)"),
			table("empty", "CREATE TABLE empty (a int)"),
			{
				Kind: KindProcedure, Name: "p", Stmts: []string{"CREATE PROCEDURE p() BEGIN SELECT 1; END"},
				Term: "//", Before: "DELIMITER //\n", After: "\nDELIMITER ;",
			},
		},
		Records: map[string]*Records{
			"t":     {Columns: []string{"a", "b"}, Rows: rows, Insert: "INSERT INTO t (a, b) VALUES"},
			"empty": {Columns: []string{"a"}, Insert: "INSERT INTO empty (a) VALUES"},
		},
	}

	exp := `CREATE SEQUENCE seq;
SELECT setval('seq', 3);

CREATE TABLE t (a int, b int);

INSERT INTO t (a, b) VALUES
  (a, NULL),
  (b, NULL),
  (c, NULL),
  (d, NULL),
  (e, NULL),
  (f, NULL),
  (g, NULL),
  (h, NULL),
  (i, NULL),
  (j, NULL);

INSERT INTO t (a, b) VALUES
  (k, NULL),
  (l, NULL);

CREATE TABLE empty (a int);

DELIMITER //
CREATE PROCEDURE p() BEGIN SELECT 1; END//
DELIMITER ;

CREATE TRIGGER trg;

`
	var b strings.Builder
	if err := Dump(&b, ss); err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff(exp, b.String()); d != "" {
		t.Error(d)
	}
}
