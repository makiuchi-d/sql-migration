package pg

import (
	"slices"
	"testing"
)

func TestStatements(t *testing.T) {
	tests := map[string]struct {
		src string
		exp []string
	}{
		"simple": {
			"SELECT 1; SELECT 2;\n\n",
			[]string{"SELECT 1", "SELECT 2"},
		},
		"no last semicolon": {
			"SELECT 1;\nSELECT 2 \n",
			[]string{"SELECT 1", "SELECT 2"},
		},
		"empty statements": {
			";;  ; SELECT 1;;",
			[]string{"SELECT 1"},
		},
		"line comment": {
			"-- a; b\nSELECT 1; -- c;\nSELECT 2--;\n;",
			[]string{"SELECT 1", "SELECT 2--;"},
		},
		"nested block comment": {
			"/* a /* b; */ c; */ SELECT 1 /* ; /* ; */ ; */; SELECT 2",
			[]string{"SELECT 1 /* ; /* ; */ ; */", "SELECT 2"},
		},
		"string": {
			"SELECT 'a;b', 'c''d;e'; SELECT 2",
			[]string{"SELECT 'a;b', 'c''d;e'", "SELECT 2"},
		},
		"backslash is not escape": {
			`SELECT 'a\'; SELECT 2`,
			[]string{`SELECT 'a\'`, "SELECT 2"},
		},
		"E string": {
			`SELECT E'a\';b', e'\\'; SELECT 2`,
			[]string{`SELECT E'a\';b', e'\\'`, "SELECT 2"},
		},
		"identifier ending with E": {
			`SELECT some'\'; SELECT 2`,
			[]string{`SELECT some'\'`, "SELECT 2"},
		},
		"quoted identifier": {
			`SELECT "a;""b"; SELECT 2`,
			[]string{`SELECT "a;""b"`, "SELECT 2"},
		},
		"dollar quote": {
			"CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; $$ LANGUAGE sql; SELECT 2",
			[]string{"CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; $$ LANGUAGE sql", "SELECT 2"},
		},
		"tagged dollar quote": {
			"DO $body$ BEGIN PERFORM $$;$$; END $body$; SELECT 2",
			[]string{"DO $body$ BEGIN PERFORM $$;$$; END $body$", "SELECT 2"},
		},
		"parameter is not dollar quote": {
			"PREPARE p AS SELECT $1; SELECT $2",
			[]string{"PREPARE p AS SELECT $1", "SELECT $2"},
		},
		"dollar in identifier": {
			"SELECT a$b$c; SELECT 2",
			[]string{"SELECT a$b$c", "SELECT 2"},
		},
		"starts with quoted": {
			`"t".a; 'x;y'`,
			[]string{`"t".a`, `'x;y'`},
		},
		"unterminated": {
			"SELECT 'abc; SELECT 2",
			[]string{"SELECT 'abc; SELECT 2"},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var stmts []string
			for s := range Statements([]byte(test.src)) {
				stmts = append(stmts, s)
			}
			if !slices.Equal(stmts, test.exp) {
				t.Errorf("%q wants %q", stmts, test.exp)
			}
		})
	}
}
