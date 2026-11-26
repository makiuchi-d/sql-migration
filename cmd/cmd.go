// Package cmd is the helpers for the commands, migy and pigy.
//
// The cobra command trees and the connection flags are written in each
// command, since the names and the options differ.
package cmd

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/makiuchi-d/sql-migration"
	"github.com/makiuchi-d/sql-migration/dialect"
	"github.com/makiuchi-d/sql-migration/migrations"
)

// Flags is the values of the common flags.
type Flags struct {
	Dir   string
	Quiet bool
	Num   int
	Force bool
}

// Out returns the writer for the progress messages.
func (f *Flags) Out() io.Writer {
	if f.Quiet {
		return io.Discard
	}
	return os.Stdout
}

// numValue parses integer flags as 10-based number (000010 => 10)
type numValue int

var _ pflag.Value = (*numValue)(nil)

func (n *numValue) Set(s string) error {
	v, err := strconv.ParseInt(s, 10, 64)
	*n = numValue(v)
	return err
}

func (n *numValue) Type() string { return "int" }

func (n *numValue) String() string { return strconv.Itoa(int(*n)) }

// NumVarP defines a flag of a 10-based number. -1 means unspecified.
func NumVarP(fs *pflag.FlagSet, p *int, name, short, defvalue, usage string) {
	*p = -1
	f := fs.VarPF((*numValue)(p), name, short, usage)
	f.DefValue = defvalue
}

// AddPersistentFlags adds --dir and --quiet.
// dirShort is the shorthand of --dir ("" for none).
func (f *Flags) AddPersistentFlags(c *cobra.Command, dirShort string) {
	fs := c.PersistentFlags()
	fs.BoolP("help", "", false, "help for this command") // disable shorthand -h
	fs.StringVarP(&f.Dir, "dir", dirShort, ".", "directory with migration files")
	fs.BoolVarP(&f.Quiet, "quiet", "q", false, "suppress progress messages")
}

// AddFlagNumber adds --number.
func (f *Flags) AddFlagNumber(c *cobra.Command) {
	NumVarP(c.Flags(), &f.Num, "number", "n", "latest", "target migration number")
}

// AddFlagForce adds --force.
func (f *Flags) AddFlagForce(c *cobra.Command) {
	c.Flags().BoolVarP(&f.Force, "force", "f", false, "overwrite the output file if it exists")
}

// Version returns the version of the module.
func Version() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		return bi.Main.Version
	}
	return "(devel)"
}

// ExitError is an error with an exit code, without a message.
type ExitError int

func (e ExitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// Main executes the command and exits with the status.
func Main(c *cobra.Command) {
	err := c.Execute()
	if err == nil {
		return
	}
	var ee ExitError
	if errors.As(err, &ee) {
		os.Exit(int(ee))
	}
	fmt.Fprintln(os.Stderr, "error:", err.Error())
	os.Exit(255)
}

// Init writes the initial migration file.
func Init(f *Flags, name string, d dialect.Dialect) error {
	return sqlmigration.Init(f.Out(), f.Dir, name, f.Force, d)
}

// Create writes a new pair of up/down migration files.
func Create(f *Flags, args []string, name string, d dialect.Dialect) error {
	if len(args) == 0 {
		return errors.New("title is required")
	}
	return sqlmigration.Create(f.Out(), f.Dir, args[0], f.Num, name, d)
}

// Check checks the migration of --number.
func Check(f *Flags, d dialect.Dialect) error {
	diff, err := sqlmigration.Check(f.Out(), f.Dir, f.Num, d)
	if err != nil {
		return err
	}
	if diff != "" {
		fmt.Println(diff)
		fmt.Println("check failed")
		return ExitError(1)
	}
	return nil
}

// CheckFrom checks each migration from `from` to --number.
// Each check runs in a subprocess to release the in-process database.
func CheckFrom(f *Flags, from int) error {
	to := f.Num
	if to >= 0 && from > to {
		return fmt.Errorf("--from (%06d) must be less than %06d", from, to)
	}
	migs, err := migrations.Load(f.Dir)
	if err != nil {
		return err
	}
	i, err := migs.FindNumber(from)
	if err != nil {
		return err
	}
	migs = migs[i:]
	if to >= 0 {
		j, err := migs.FindNumber(to)
		if err != nil {
			return err
		}
		migs = migs[:j+1]
	}

	args := []string{"check", "-n", "", "--dir", f.Dir}
	if f.Quiet {
		args = append(args, "-q")
	}
	for _, mig := range migs {
		fmt.Fprintf(f.Out(), "==== check %06d\n", mig.Number)
		args[2] = strconv.Itoa(mig.Number)
		c := exec.Command(os.Args[0], args...)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			if c.ProcessState != nil {
				return ExitError(c.ProcessState.ExitCode())
			}
			return err
		}
	}
	return nil
}

// Snapshot writes the *.all.sql of --number.
func Snapshot(f *Flags, name string, d dialect.Dialect) error {
	path, err := sqlmigration.SnapshotFile(f.Dir, f.Num)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil && !f.Force {
		return fmt.Errorf("file exists: %s", path)
	}

	var buf bytes.Buffer
	if err := sqlmigration.Snapshot(f.Out(), &buf, f.Dir, f.Num, name, d); err != nil {
		return err
	}
	fmt.Fprintln(f.Out(), "writing:", path)
	return os.WriteFile(path, buf.Bytes(), 0666)
}

// List prints the files to apply to reach --number.
func List(f *Flags, db *sqlx.DB, d dialect.Dialect) error {
	files, err := sqlmigration.ListFiles(db, f.Dir, f.Num, d)
	if err != nil {
		return err
	}
	for _, file := range files {
		fmt.Println(filepath.Join(f.Dir, file))
	}
	return nil
}

// Status prints the status of each migration. db can be nil.
func Status(f *Flags, db *sqlx.DB, d dialect.Dialect) error {
	st, err := sqlmigration.Status(db, f.Dir, d)
	if err != nil {
		return err
	}
	fmt.Print(st)
	return nil
}

// Apply applies the migration files to reach --number after a confirmation.
func Apply(f *Flags, db *sqlx.DB, yes bool, d dialect.Dialect) error {
	files, err := sqlmigration.ListFiles(db, f.Dir, f.Num, d)
	if err != nil {
		return err
	}
	out := f.Out()
	if len(files) == 0 {
		fmt.Fprintln(out, "Nothing to do.")
		return nil
	}
	if !yes {
		fmt.Println("The following migration files will be applied:")
		for _, file := range files {
			fmt.Println(" -", file)
		}
		fmt.Print("Do you want to continue? [y/N]: ")
		s := bufio.NewScanner(os.Stdin)
		s.Scan()
		in := strings.ToLower(strings.TrimSpace(s.Text()))
		if in != "y" && in != "yes" {
			fmt.Println("Abort.")
			return ExitError(1)
		}
	}
	return sqlmigration.Apply(out, db, f.Dir, f.Num, d)
}
