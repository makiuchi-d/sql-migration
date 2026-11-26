package dbstate

import (
	"io"
)

// InsertChunk is the number of rows put in one INSERT statement.
const InsertChunk = 10

// Dump writes the snapshot as a SQL file.
//
// Each table is followed by its records. The objects that must come after
// the records (see Kind.AfterRecords) are written last.
func Dump(w io.Writer, ss *Snapshot) error {
	var after []*Object
	for _, o := range ss.Objects {
		if o.Kind.AfterRecords() {
			after = append(after, o)
			continue
		}
		if _, err := io.WriteString(w, o.Text()+"\n\n"); err != nil {
			return err
		}
		if rec, ok := ss.Records[o.Name]; ok {
			for _, s := range rec.Inserts(InsertChunk) {
				if _, err := io.WriteString(w, s+"\n\n"); err != nil {
					return err
				}
			}
		}
	}
	for _, o := range after {
		if _, err := io.WriteString(w, o.Text()+"\n\n"); err != nil {
			return err
		}
	}
	return nil
}
