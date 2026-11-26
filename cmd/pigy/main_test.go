package main

import "testing"

func TestConnString(t *testing.T) {
	tests := []struct {
		dbname, host string
		port         int
		user, exp    string
	}{
		{"", "", 0, "", ""},
		{"app", "", 0, "", "dbname='app'"},
		{"app", "db.local", 5433, "bob", "dbname='app' host='db.local' port=5433 user='bob'"},
		{"it's", "", 0, `a\b`, `dbname='it\'s' user='a\\b'`},
		{"host=x dbname=app", "y", 0, "", "host=x dbname=app host='y'"},
		{"postgres://u:pw@x:1/app?sslmode=disable", "", 0, "", "postgres://u:pw@x:1/app?sslmode=disable"},
		{"postgres://u:pw@x:1/app", "y", 2, "v", "postgres://v:pw@y:2/app"},
		{"postgresql://x/app", "", 0, "v", "postgresql://v@x/app"},
	}
	for _, test := range tests {
		s, err := connString(test.dbname, test.host, test.port, test.user)
		if err != nil {
			t.Errorf("%q: %v", test.dbname, err)
			continue
		}
		if s != test.exp {
			t.Errorf("%q: %q wants %q", test.dbname, s, test.exp)
		}
	}
}
