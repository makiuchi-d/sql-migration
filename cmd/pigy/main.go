// pigy is a standalone database migration helper for PostgreSQL.
package main

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/spf13/cobra"

	"github.com/makiuchi-d/sql-migration"
	"github.com/makiuchi-d/sql-migration/cmd"
	"github.com/makiuchi-d/sql-migration/dialect/pg"
)

const name = "pigy"

var (
	d     = pg.New()
	flags cmd.Flags

	dbHost string
	dbPort int
	dbUser string
	dbName string

	checkFrom int
	applyYes  bool
)

var root = &cobra.Command{
	Use:           name,
	Short:         "A standalone database migration helper for PostgreSQL",
	SilenceErrors: true,
	SilenceUsage:  true,
	Version:       cmd.Version(),
	PersistentPostRunE: func(c *cobra.Command, args []string) error {
		return d.Close()
	},
}

func main() {
	cmd.Main(root)
}

func init() {
	// -d is the database name as psql, so --dir has no shorthand.
	flags.AddPersistentFlags(root, "")

	cmdInit := &cobra.Command{
		Use:   "init",
		Short: "Generate the initial migration SQL file",
		Long: `Generate the initial migration SQL file.
This file sets up the initial state of the database,
including the _migrations table used to track applied migrations.`,
		RunE: func(c *cobra.Command, args []string) error {
			return cmd.Init(&flags, name, d)
		},
	}
	flags.AddFlagForce(cmdInit)

	cmdCreate := &cobra.Command{
		Use:   "create [flags] <TITLE>",
		Short: "Create a new pair of up/down SQL migration files",
		Long: `Create a new pair of up/down SQL migration files.
The up file defines the forward migration. The down file contains
the corresponding rollback, ensuring changes can be reversed.`,
		RunE: func(c *cobra.Command, args []string) error {
			return cmd.Create(&flags, args, name, d)
		},
	}
	flags.AddFlagNumber(cmdCreate)

	cmdCheck := &cobra.Command{
		Use:   "check [flags]",
		Short: "Check if an up/down migration pair is reversible",
		Long: `Check if an up/down migration pair is reversible.
Applies the up migration to a temporary database and then rolls it back
using the down migration to verify that no differences remain.`,
		RunE: func(c *cobra.Command, args []string) error {
			if checkFrom >= 0 {
				return cmd.CheckFrom(&flags, checkFrom)
			}
			return cmd.Check(&flags, d)
		},
	}
	flags.AddFlagNumber(cmdCheck)
	cmd.NumVarP(cmdCheck.Flags(), &checkFrom, "from", "", "n", "check each migration from this to --number")

	cmdSnapshot := &cobra.Command{
		Use:   "snapshot",
		Short: "Generate a SQL snapshot at the specified migration point",
		Long: `Applies up migrations sequentially and generates a SQL file
that reproduces the database state at that point.`,
		RunE: func(c *cobra.Command, args []string) error {
			return cmd.Snapshot(&flags, name, d)
		},
	}
	flags.AddFlagNumber(cmdSnapshot)
	flags.AddFlagForce(cmdSnapshot)

	cmdList := &cobra.Command{
		Use:   "list [flags] [DUMP_FILE | -d DBNAME]",
		Short: "List migration files needed to reach the target state",
		Long: `Lists migration files needed to reach the target migration number by
comparing the migration directory with the database or dump file.
The database is connected when any of -d, -h, -p and -U is given.`,
		RunE: func(c *cobra.Command, args []string) error {
			db, err := openDBOrDumpFile(c, args)
			if err != nil {
				return err
			}
			if db == nil {
				return errors.New("data source or dump file is required")
			}
			defer db.Close()
			return cmd.List(&flags, db, d)
		},
	}
	flags.AddFlagNumber(cmdList)
	addFlagsForDB(cmdList)

	cmdStatus := &cobra.Command{
		Use:   "status [flags] [DUMP_FILE | -d DBNAME]",
		Short: "Show the status of each migration",
		Long: `Show the status of each migration.
Compares migration files with the given database or dump file
and summarizes their current status.
The database is connected when any of -d, -h, -p and -U is given.`,
		RunE: func(c *cobra.Command, args []string) error {
			db, err := openDBOrDumpFile(c, args)
			if err != nil {
				return err
			}
			if db != nil {
				defer db.Close()
			}
			return cmd.Status(&flags, db, d)
		},
	}
	addFlagsForDB(cmdStatus)

	cmdApply := &cobra.Command{
		Use:   "apply [flags] [-d DBNAME]",
		Short: "Apply migration files to reach the target database state",
		Long: `Apply migration files to the target database in order.
Continues from the last applied migration and moves forward or backward
to match the target migration number.
This command requires a live database connection.
The connection parameters not given by the flags are taken from the
environment variables (PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSWORD, ...)
and the password file (~/.pgpass) as libpq does.`,
		RunE: func(c *cobra.Command, args []string) error {
			db, err := openDB()
			if err != nil {
				return err
			}
			defer db.Close()
			return cmd.Apply(&flags, db, applyYes, d)
		},
	}
	flags.AddFlagNumber(cmdApply)
	addFlagsForDB(cmdApply)
	cmdApply.Flags().BoolVarP(&applyYes, "yes", "y", false, `assume "yes" as answer to all prompts`)

	cmdVersion := &cobra.Command{
		Use:   "version",
		Short: "Show version",
		RunE: func(c *cobra.Command, args []string) error {
			fmt.Println(name, "version", cmd.Version())
			return nil
		},
	}

	root.AddCommand(cmdInit, cmdCreate, cmdCheck, cmdSnapshot, cmdList, cmdStatus, cmdApply, cmdVersion)
}

// addFlagsForDB adds the connection flags compatible with psql.
// There is no password flag: use PGPASSWORD or ~/.pgpass.
func addFlagsForDB(c *cobra.Command) {
	fs := c.Flags()
	fs.StringVarP(&dbHost, "host", "h", "", "database server host (PGHOST)")
	fs.IntVarP(&dbPort, "port", "p", 0, "database server port (PGPORT)")
	fs.Lookup("port").DefValue = "5432"
	fs.StringVarP(&dbUser, "username", "U", "", "database user name (PGUSER)")
	fs.StringVarP(&dbName, "dbname", "d", "", "database name, or a connection URI / conninfo string (PGDATABASE)")
}

func openDBOrDumpFile(c *cobra.Command, args []string) (*sqlx.DB, error) {
	fs := c.Flags()
	if fs.Changed("dbname") || fs.Changed("host") || fs.Changed("port") || fs.Changed("username") {
		return openDB()
	}
	if len(args) > 0 {
		return sqlmigration.OpenDumpFile(d, args[0])
	}
	return nil, nil
}

func openDB() (*sqlx.DB, error) {
	cs, err := connString(dbName, dbHost, dbPort, dbUser)
	if err != nil {
		return nil, err
	}
	return sqlx.Open("pgx", cs)
}

// connString builds the connection string for pgx.
// dbname may be a URI or a conninfo string as psql -d accepts;
// then host, port and user override it.
// The parameters left empty are taken from the environment by pgx.
func connString(dbname, host string, port int, user string) (string, error) {
	if strings.HasPrefix(dbname, "postgres://") || strings.HasPrefix(dbname, "postgresql://") {
		u, err := url.Parse(dbname)
		if err != nil {
			return "", err
		}
		h, p := u.Hostname(), u.Port()
		if host != "" {
			h = host
		}
		if port != 0 {
			p = strconv.Itoa(port)
		}
		u.Host = h
		if p != "" {
			u.Host += ":" + p
		}
		if user != "" {
			if pw, ok := u.User.Password(); ok {
				u.User = url.UserPassword(user, pw)
			} else {
				u.User = url.User(user)
			}
		}
		return u.String(), nil
	}

	var kv []string
	if strings.Contains(dbname, "=") {
		kv = append(kv, dbname)
	} else if dbname != "" {
		kv = append(kv, "dbname="+quoteConnValue(dbname))
	}
	if host != "" {
		kv = append(kv, "host="+quoteConnValue(host))
	}
	if port != 0 {
		kv = append(kv, "port="+strconv.Itoa(port))
	}
	if user != "" {
		kv = append(kv, "user="+quoteConnValue(user))
	}
	return strings.Join(kv, " "), nil
}

func quoteConnValue(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return "'" + s + "'"
}
