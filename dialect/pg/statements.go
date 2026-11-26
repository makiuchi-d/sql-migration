package pg

import (
	"bytes"
	"iter"
	"strings"
)

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f'
}

func isIdentStart(c byte) bool {
	return c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c >= 0x80
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || '0' <= c && c <= '9' || c == '$'
}

// lineComment returns the length of a "--" comment including the newline.
func lineComment(input []byte) int {
	if !bytes.HasPrefix(input, []byte("--")) {
		return 0
	}
	if i := bytes.IndexByte(input, '\n'); i >= 0 {
		return i + 1
	}
	return len(input)
}

// blockComment returns the length of a "/* */" comment. They can be nested.
func blockComment(input []byte) int {
	if !bytes.HasPrefix(input, []byte("/*")) {
		return 0
	}
	depth := 0
	for i := 0; i < len(input)-1; i++ {
		switch {
		case input[i] == '/' && input[i+1] == '*':
			depth++
			i++
		case input[i] == '*' && input[i+1] == '/':
			depth--
			i++
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(input)
}

// quoted returns the length of a '...' string or a "..." identifier.
// A doubled quote inside is read as two adjacent quoted texts.
// With escape, a backslash escapes the next character (E'...').
func quoted(input []byte, escape bool) int {
	q := input[0]
	for i := 1; i < len(input); i++ {
		switch input[i] {
		case q:
			return i + 1
		case '\\':
			if escape {
				i++
			}
		}
	}
	return len(input)
}

// dollarTag returns the tag ($tag$ or $$) at the head of the input.
func dollarTag(input []byte) []byte {
	if len(input) < 2 || input[0] != '$' {
		return nil
	}
	if input[1] == '$' {
		return input[:2]
	}
	if !isIdentStart(input[1]) {
		return nil // $1 is a parameter
	}
	for i := 2; i < len(input); i++ {
		if input[i] == '$' {
			return input[:i+1]
		}
		if !isIdentChar(input[i]) {
			return nil
		}
	}
	return nil
}

// dollarQuoted returns the length of a dollar-quoted string.
func dollarQuoted(input []byte) int {
	tag := dollarTag(input)
	if tag == nil {
		return 0
	}
	if i := bytes.Index(input[len(tag):], tag); i >= 0 {
		return len(tag) + i + len(tag)
	}
	return len(input)
}

// Statements splits a PostgreSQL script into statements.
// Each statement is returned without the semicolon.
func Statements(input []byte) iter.Seq[string] {
	return func(yield func(string) bool) {
		inStmt := false
		start := 0
		p := 0
		emit := func(end int) bool {
			return yield(strings.TrimRight(string(input[start:end]), " \t\r\n\f"))
		}
		for p < len(input) {
			c := input[p]
			if !inStmt && isSpace(c) {
				p++
				continue
			}
			if c == ';' {
				if inStmt && !emit(p) {
					return
				}
				inStmt = false
				p++
				continue
			}
			if n := lineComment(input[p:]); n > 0 {
				p += n
				continue
			}
			if n := blockComment(input[p:]); n > 0 {
				p += n
				continue
			}

			if !inStmt {
				inStmt = true
				start = p
			}

			// identifiers may contain '$' and may end with 'E'
			afterIdent := p > 0 && isIdentChar(input[p-1])

			switch {
			case c == '\'':
				escape := p > 0 && (input[p-1] == 'E' || input[p-1] == 'e') &&
					(p == 1 || !isIdentChar(input[p-2]))
				p += quoted(input[p:], escape)
				continue
			case c == '"':
				p += quoted(input[p:], false)
				continue
			case c == '$' && !afterIdent:
				if n := dollarQuoted(input[p:]); n > 0 {
					p += n
					continue
				}
			}
			p++
		}

		if inStmt {
			emit(p)
		}
	}
}
