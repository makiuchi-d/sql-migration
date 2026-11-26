package mysql

import (
	"bytes"
	"iter"
	"strings"
)

func skipSpaces(input []byte) (length int) {
	for i, c := range input {
		switch c {
		case ' ', '\t', '\r', '\n':
		default:
			return i
		}
	}
	return len(input)
}

func singlelineComment(input []byte) (length int) {
	if !bytes.HasPrefix(input, []byte("--")) {
		return 0
	}
	for i := 2; i < len(input); i++ {
		if input[i] == '\n' {
			return i + 1
		}
	}
	return len(input)
}

func multilineComment(input []byte) (length int) {
	if !bytes.HasPrefix(input, []byte("/*")) {
		return 0
	}
	for i := 2; i < len(input)-1; i++ {
		if input[i] == '*' && input[i+1] == '/' {
			return i + 2
		}
	}
	return len(input)
}

func quotedLiteral(input []byte) (length int) {
	quote := input[0]
	switch quote {
	case '\'', '"', '`':
	default:
		return 0
	}
	for i := 1; i < len(input); i++ {
		if input[i] == quote {
			return i + 1
		}
		if input[i] == '\\' {
			i++
		}
	}
	return len(input)
}

func delimiterCmd(input []byte) (length int) {
	const cmd = "DELIMITER"
	p := 0
	if bytes.HasPrefix(input, []byte("\\d")) {
		p += 2
	} else if len(input) > len(cmd)+1 {
		if strings.ToUpper(string(input[:len(cmd)])) != cmd {
			return 0
		}
		if c := input[len(cmd)]; c != ' ' && c != '\t' {
			return 0
		}
		p += len(cmd) + 1
	}
	for p < len(input) && (input[p] == ' ' || input[p] == '\t') {
		p++
	}
	return p
}

func changeDelimiter(input []byte) (delimiter []byte, length int) {
	p := delimiterCmd(input)
	if p == 0 {
		return nil, 0
	}

	// DELIMITER command only takes the rest of the line.
	line := input[p:]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	if len(line) == 0 {
		return nil, 0
	}

	// quoted delimiter
	if n := quotedLiteral(line); n > 0 {
		delim := line[1 : n-1]
		if line[0] != line[n-1] {
			// unclosed
			delim = line[1:n]
		}
		return delim, p + n
	}

	for i := range len(line) {
		switch line[i] {
		case ' ', '\t', '\r':
			return line[:i], p + i
		}
	}

	return line, p + len(line)
}

// Statements splits a MySQL script into statements.
// It understands the DELIMITER command of the mysql client.
// Each statement is returned without its delimiter.
func Statements(input []byte) iter.Seq[string] {
	return func(yield func(string) bool) {
		delim := []byte{';'}
		inStmt := false
		start := 0
		p := 0
		emit := func(end int) bool {
			s := strings.TrimRight(string(input[start:end]), " \t\r\n")
			return yield(s)
		}
		for p < len(input) {
			if !inStmt {
				if n := skipSpaces(input[p:]); n > 0 {
					p += n
					continue
				}
			}

			if bytes.HasPrefix(input[p:], delim) {
				if inStmt {
					if !emit(p) {
						return
					}
				}
				inStmt = false
				p += len(delim)
				continue
			}

			// the DELIMITER command is a client command, not a part of a statement.
			if !inStmt {
				if d, n := changeDelimiter(input[p:]); n > 0 {
					p += n
					delim = d
					continue
				}
			}

			if n := singlelineComment(input[p:]); n > 0 {
				p += n
				continue
			}
			if n := multilineComment(input[p:]); n > 0 {
				p += n
				continue
			}

			if !inStmt {
				inStmt = true
				start = p
			}

			if n := quotedLiteral(input[p:]); n > 0 {
				p += n
				continue
			}
			p++
		}

		if inStmt && start != p {
			emit(p)
		}
	}
}
