// migy is a standalone database migration helper for MySQL.
package main

import (
	"errors"
	"fmt"
	"os/user"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/spf13/cobra"

	"github.com/makiuchi-d/sql-migration"
	"github.com/makiuchi-d/sql-migration/cmd"
	"github.com/makiuchi-d/sql-migration/dialect/mysql"
)

const name = "migy"

var (
	d     = mysql.Dialect{}
	flags cmd.Flags

	dbHost string
	dbPort int
	dbUser string
	dbPass string
	dbName string
	dbDsn  string

	checkFrom int
	applyYes  bool
)

var root = &cobra.Command{
	Use:           name,
	Short:         "A standalone database migration helper for MySQL",
	SilenceErrors: true,
	SilenceUsage:  true,
	Version:       cmd.Version(),
}

func main() {
	cmd.Main(root)
}

func init() {
	flags.AddPersistentFlags(root, "d")

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
		Use:   "list [flags] [DUMP_FILE | --host HOST DB_NAME | --dsn DSN]",
		Short: "List migration files needed to reach the target state",
		Long: `Lists migration files needed to reach the target migration number by
comparing the migration directory with the database or dump file.`,
		RunE: func(c *cobra.Command, args []string) error {
			db, err := openDBOrDumpFile(args)
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
		Use:   "status [flags] [DUMP_FILE | --host HOST DB_NAME | --dsn DSN]",
		Short: "Show the status of each migration",
		Long: `Show the status of each migration.
Compares migration files with the given database or dump file
and summarizes their current status.`,
		RunE: func(c *cobra.Command, args []string) error {
			db, err := openDBOrDumpFile(args)
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
		Use:   "apply [flags] [--host HOST DB_NAME | --dsn DSN]",
		Short: "Apply migration files to reach the target database state",
		Long: `Apply migration files to the target database in order.
Continues from the last applied migration and moves forward or backward
to match the target migration number.
This command requires a live database connection.`,
		RunE: func(c *cobra.Command, args []string) error {
			db, err := openDB(args)
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

// addFlagsForDB adds the connection flags compatible with the mysql client.
func addFlagsForDB(c *cobra.Command) {
	fs := c.Flags()
	fs.StringVarP(&dbHost, "host", "h", "", "database host")
	fs.IntVarP(&dbPort, "port", "P", 3306, "database port")
	fs.StringVarP(&dbUser, "user", "u", "", "database user")
	fs.StringVarP(&dbPass, "password", "p", "", "database password")
	fs.StringVarP(&dbName, "database", "D", "", "database name")
	fs.StringVarP(&dbDsn, "dsn", "", "", "data source name (e.g. user:pass@tcp(host:port)/dbname)")
}

// openDB opens the database given by --host or --dsn.
func openDB(args []string) (*sqlx.DB, error) {
	if dbDsn != "" {
		return openDSN(dbDsn)
	}
	if dbHost != "" {
		return openHost(args)
	}
	return nil, errors.New("--host or --dsn required")
}

// openDBOrDumpFile opens the database given by --host or --dsn, or the dump
// file given as the argument. It returns nil if none is given.
func openDBOrDumpFile(args []string) (*sqlx.DB, error) {
	if dbDsn != "" || dbHost != "" {
		return openDB(args)
	}
	if len(args) > 0 {
		return sqlmigration.OpenDumpFile(d, args[0])
	}
	return nil, nil
}

func openHost(args []string) (*sqlx.DB, error) {
	db := dbName
	if db == "" {
		if len(args) < 1 {
			return nil, errors.New("database name is required")
		}
		db = args[0]
	}
	usr := dbUser
	if usr == "" {
		u, err := user.Current()
		if err != nil {
			return nil, err
		}
		usr = u.Username
	}
	c := mysqldrv.NewConfig()
	c.User = usr
	c.Passwd = dbPass
	c.Net = "tcp"
	c.Addr = fmt.Sprintf("%s:%d", dbHost, dbPort)
	c.DBName = db
	c.ParseTime = true
	return sqlx.Open("mysql", c.FormatDSN())
}

func openDSN(dsn string) (*sqlx.DB, error) {
	c, err := mysqldrv.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	c.ParseTime = true
	return sqlx.Open("mysql", c.FormatDSN())
}
