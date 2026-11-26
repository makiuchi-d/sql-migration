package dbstate

import (
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/makiuchi-d/anydiff"
)

// DiffOptions controls Diff.
type DiffOptions struct {
	Ignores map[string][]string // table name -> column names to ignore ("*" for all)
}

// Diff compares two snapshots and returns the differences in a human readable
// form, or "" when they are the same.
//
// Objects are matched by (Kind, Name) and their Stmts are compared line by
// line. Records are compared only for the tables whose Stmts are the same.
// Deps, the text metadata and the order of Objects are not compared.
func Diff(before, after *Snapshot, opt DiffOptions) string {
	var sb strings.Builder

	bobjs := make(map[Ref]*Object, len(before.Objects))
	for _, o := range before.Objects {
		bobjs[o.Ref()] = o
	}
	found := make(map[Ref]bool, len(after.Objects))

	for _, a := range after.Objects {
		ref := a.Ref()
		found[ref] = true
		b, ok := bobjs[ref]
		if !ok {
			fmt.Fprintf(&sb, "unexpected %s %q found\n", a.Kind, a.Name)
			continue
		}

		bl, al := stmtLines(b.Stmts), stmtLines(a.Stmts)
		edit := anydiff.Diff(bl, al, anydiff.Cmp)
		if edit.Distance() != 0 {
			fmt.Fprintf(&sb, "%s %q differs:\n", a.Kind, a.Name)
			writeEdit(&sb, edit, bl, al)
			continue
		}

		if a.Kind == KindTable {
			diffRecords(&sb, a.Name, before.Records[a.Name], after.Records[a.Name], opt.Ignores[a.Name])
		}
	}

	for _, b := range before.Objects {
		if !found[b.Ref()] {
			fmt.Fprintf(&sb, "missing %s %q\n", b.Kind, b.Name)
		}
	}

	return sb.String()
}

func stmtLines(stmts []string) []string {
	var lines []string
	for _, s := range stmts {
		lines = append(lines, strings.Split(s, "\n")...)
	}
	return lines
}

func diffRecords(sb *strings.Builder, table string, before, after *Records, ignores []string) {
	if slices.Contains(ignores, "*") {
		return
	}
	if before == nil {
		before = &Records{}
	}
	if after == nil {
		after = &Records{}
	}

	cols := after.Columns
	cmp := func(a, b *Row) bool {
		if len(*a) != len(*b) {
			return false
		}
		for i := range *a {
			if i < len(cols) && slices.Contains(ignores, cols[i]) {
				continue
			}
			if (*a)[i] != (*b)[i] {
				return false
			}
		}
		return true
	}

	edit := anydiff.Diff(before.Rows, after.Rows, cmp)
	if edit.Distance() == 0 {
		return
	}

	fmt.Fprintf(sb, "records in %q differs:\n", table)
	writeEdit(sb, edit, before.Rows, after.Rows)
}

// writeEdit writes the changed lines with 2 lines of context around them.
// Lines far from any change are elided as "...".
func writeEdit[A, B any](sb *strings.Builder, edit anydiff.Edit, a []A, b []B) {

	next, stop := iter.Pull(func(yield func(string) bool) {
		i, j := 0, 0
		for _, op := range edit {
			switch op {
			case anydiff.Deletion:
				if !yield(fmt.Sprintf("-%v\n", a[i])) {
					return
				}
				i++
			case anydiff.Addition:
				if !yield(fmt.Sprintf("+%v\n", b[j])) {
					return
				}
				j++
			case anydiff.Keep:
				if !yield(fmt.Sprintf(" %v\n", b[j])) {
					return
				}
				i++
				j++
			}
		}
	})
	defer stop()

	cnt := 0

	buf, ok := next()
	if !ok {
		return
	}
	if buf[0] != ' ' {
		cnt = 2
	}

	for {
		s, ok := next()
		if !ok {
			break
		}
		if s[0] != ' ' {
			cnt = 3
		}

		if cnt > 0 {
			sb.WriteString(buf)
			cnt--
		} else if cnt == 0 {
			sb.WriteString("...\n")
			cnt--
		}

		buf = s
	}

	if cnt >= 1 {
		sb.WriteString(buf)
	}
	if cnt == 0 {
		sb.WriteString("...\n")
	}
}
