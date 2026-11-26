package sqlmigration

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"

	"github.com/makiuchi-d/sql-migration/dialect"
	"github.com/makiuchi-d/sql-migration/migrations"
)

// InitFile is the name of the initial migration file.
const InitFile = "000000_init.all.sql"

// Init writes the initial migration file, which creates the '_migrations' table.
// name is the tool name written in the signature line.
func Init(w io.Writer, dir, name string, overwrite bool, d dialect.Dialect) error {
	path := filepath.Join(dir, InitFile)
	flag := os.O_CREATE | os.O_RDWR | os.O_TRUNC
	if !overwrite {
		flag |= os.O_EXCL
	}
	info(w, "writing:", path)
	f, err := os.OpenFile(path, flag, 0666)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprint(f, signature(name), "\n\n", d.InitSQL())
	return err
}

func nextNum(n int) int {
	return n + 10 - n%10
}

// Create writes a new pair of up/down migration files.
// The number is num, or the next multiple of 10 of the last migration if num < 0.
// name is the tool name written in the signature line.
func Create(w io.Writer, dir, title string, num int, name string, d dialect.Dialect) error {
	migs, err := migrations.Load(dir)
	if err != nil {
		return err
	}

	if num < 0 {
		num = nextNum(migs.Last().Number)
	} else if i, err := migs.FindNumber(num); err == nil {
		return fmt.Errorf("duplicate number: %06d %v", num, migs[i].Title)
	}

	mig := migrations.Migration{
		Number: num,
		Title:  title,
	}
	sig := signature(name) + "\n"
	if err := writeTemplate(w, dir, mig.UpName(), sig+d.UpTemplate(), mig); err != nil {
		return err
	}
	return writeTemplate(w, dir, mig.DownName(), sig+d.DownTemplate(), mig)
}

func writeTemplate(w io.Writer, dir, name, tmpl string, mig migrations.Migration) error {
	t, err := template.New(name).Parse(tmpl)
	if err != nil {
		return err
	}

	path := filepath.Join(dir, name)
	info(w, "writing:", path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0666)
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, mig)
}
