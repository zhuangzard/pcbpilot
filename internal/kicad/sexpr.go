package kicad

// sexpr.go — the one tokenizer behind every KiCad S-expression reader in
// this package (parseSx for sheets/symbols, parseSexpr for boards). The
// rules follow KiCad 10's own reader, checked by re-saving hand-written
// sheets with `kicad-cli sch upgrade --force`:
//
//   - separators are space, tab, CR, LF and NUL; a line whose first
//     non-blank byte is '#' is a comment;
//   - a bare atom runs to the next separator or parenthesis (a '"' inside it
//     is an ordinary byte);
//   - a quoted string may not contain a raw LF; escapes are \" \\ \a \b \f
//     \n \r \t \v, \x with 1–2 hex digits (no digit: a plain 'x'), 1–3 octal
//     digits, and any other \c keeps the backslash;
//   - text after the top-level list is ignored (KiCad loads such files).
//
// Specctra DSN/SES (internal/pcb/specctra) has no escapes and keeps its own
// reader.

import (
	"fmt"
	"strings"
)

// maxSexprDepth caps list nesting. Real KiCad files stay under ~20 levels;
// the cap turns hostile input into an error instead of a deep stack.
const maxSexprDepth = 1000

const (
	tokEOF = iota
	tokOpen
	tokClose
	tokAtom
	tokString
)

type sexprLexer struct {
	src string
	pos int
	bol bool // only separators since the last LF (a '#' starts a comment)
}

func isSexprSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == 0 }

func (l *sexprLexer) skip() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\n':
			l.bol = true
			l.pos++
		case isSexprSpace(c):
			l.pos++
		case c == '#' && l.bol:
			if i := strings.IndexByte(l.src[l.pos:], '\n'); i >= 0 {
				l.pos += i
			} else {
				l.pos = len(l.src)
			}
		default:
			return
		}
	}
}

// next returns the next token, its value (atoms and unescaped strings) and
// its byte offset.
func (l *sexprLexer) next() (kind int, val string, off int, err error) {
	l.skip()
	off = l.pos
	if l.pos >= len(l.src) {
		return tokEOF, "", off, nil
	}
	l.bol = false
	switch l.src[l.pos] {
	case '(':
		l.pos++
		return tokOpen, "", off, nil
	case ')':
		l.pos++
		return tokClose, "", off, nil
	case '"':
		i, esc := l.pos+1, false
		for ; i < len(l.src); i++ {
			c := l.src[i]
			if c == '"' {
				break
			}
			if c == '\n' {
				return 0, "", off, fmt.Errorf("newline in string at byte %d (KiCad escapes it as \\n)", off)
			}
			if c == '\\' {
				esc = true
				i++
				if i < len(l.src) && l.src[i] == '\n' {
					return 0, "", off, fmt.Errorf("newline in string at byte %d (KiCad escapes it as \\n)", off)
				}
			}
		}
		if i >= len(l.src) {
			return 0, "", off, fmt.Errorf("unterminated string at byte %d", off)
		}
		body := l.src[l.pos+1 : i]
		l.pos = i + 1
		if esc {
			body = unescapeKiCad(body)
		}
		return tokString, body, off, nil
	default:
		i := l.pos
		for i < len(l.src) && !isSexprSpace(l.src[i]) && l.src[i] != '(' && l.src[i] != ')' {
			i++
		}
		val = l.src[l.pos:i]
		l.pos = i
		return tokAtom, val, off, nil
	}
}

// unescapeKiCad decodes the body of a KiCad quoted string (without the
// quotes) the way KiCad's reader does.
func unescapeKiCad(s string) string {
	if strings.IndexByte(s, '\\') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch e := s[i]; e {
		case '"', '\\':
			b.WriteByte(e)
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case 'x':
			v, n := 0, 0
			for n < 2 && i+1 < len(s) && hexVal(s[i+1]) >= 0 {
				i++
				v = v*16 + hexVal(s[i])
				n++
			}
			if n == 0 {
				b.WriteByte('x')
			} else {
				b.WriteByte(byte(v))
			}
		default:
			if e < '0' || e > '7' {
				b.WriteByte('\\')
				b.WriteByte(e)
				break
			}
			v := int(e - '0')
			for n := 1; n < 3 && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '7'; n++ {
				i++
				v = v*8 + int(s[i]-'0')
			}
			b.WriteByte(byte(v))
		}
	}
	return b.String()
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// sexprEvents drives a tree builder from the lexer: open(off) before a
// list's children, close(end) after them, atom(val, quoted, off) for each
// atom. It reads one top-level expression and ignores what follows (the
// caller sees where the root ends). Errors carry byte offsets.
func sexprEvents(src string, open func(off int), close func(end int), atom func(val string, quoted bool, off int)) error {
	l := &sexprLexer{src: src, bol: true}
	depth := 0
	for {
		kind, val, off, err := l.next()
		if err != nil {
			return err
		}
		switch kind {
		case tokEOF:
			if depth > 0 {
				return fmt.Errorf("unclosed '(' at end of input (%d still open)", depth)
			}
			return fmt.Errorf("empty input")
		case tokOpen:
			if depth == maxSexprDepth {
				return fmt.Errorf("lists nested deeper than %d at byte %d", maxSexprDepth, off)
			}
			depth++
			open(off)
			continue
		case tokClose:
			if depth == 0 {
				return fmt.Errorf("unexpected ')' at byte %d", off)
			}
			depth--
			close(off + 1)
		default:
			atom(val, kind == tokString, off)
		}
		if depth == 0 {
			return nil
		}
	}
}
