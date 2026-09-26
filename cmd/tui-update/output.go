package main

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// splitOutput turns a command's output into the lines of the apply pane.
//
// The output is what a terminal would have been sent, not text: apt and dpkg
// redraw a progress line in place by ending it in a carriage return and
// writing the next state over it ("(Reading database ... 5%\r(Reading
// database ... 10%\r..."). Split on newlines alone, every state of that line
// lands in the pane at once, glued together. So a carriage return does what it
// does on a terminal: the text after it replaces the line, and only the last
// state of a redrawn line is kept. A carriage return with nothing after it
// (a CRLF line end, or a redraw the program finished with) leaves the line as
// it was.
//
// Terminal control is stripped the way the kit's runner.StatusLine strips it,
// so the pane and the status line agree on what a line says: escape sequences
// and other control characters go, a cursor move to another line ends the
// line, a tab is a space, and the text drawn in the DEC line-drawing set (a
// dialog's frame) goes. Unlike the status line, blank lines between paragraphs
// are kept, because the pane is the whole output rather than one line of it.
func splitOutput(out string) []string {
	var (
		lines []string
		// line is the text of the current line; segment the text written
		// since the last carriage return, which replaces line once it has
		// something in it.
		line, segment strings.Builder
		// graphics reports being inside ESC ( 0, the DEC line-drawing set.
		graphics bool
	)
	settle := func() {
		if strings.TrimSpace(segment.String()) != "" {
			line.Reset()
			line.WriteString(segment.String())
		}
		segment.Reset()
	}
	flush := func() {
		settle()
		lines = append(lines, strings.TrimRight(line.String(), " "))
		line.Reset()
	}
	for i := 0; i < len(out); {
		r, size := utf8.DecodeRuneInString(out[i:])
		switch {
		case r == 0x1b:
			n, brk, charset := escapeSequence(out[i:])
			if charset != 0 {
				graphics = charset == '0'
			}
			// A cursor move ends a line with text on it; one that only moves
			// between blank lines leaves no blank line behind.
			if brk && strings.TrimSpace(line.String()+segment.String()) != "" {
				flush()
			}
			i += n
			continue
		case r == '\r':
			settle()
		case r == '\n' || r == '\f' || r == '\v':
			flush()
		case r == '\t':
			segment.WriteByte(' ')
		case r == utf8.RuneError && size == 1, unicode.IsControl(r):
			// Other C0 and C1 controls, DEL and bytes that are not UTF-8.
		case graphics:
			// A frame drawn as "lqqqk": not text.
		default:
			segment.WriteRune(r)
		}
		i += size
	}
	flush()
	// Trailing blank lines are the end of the output, not part of it.
	for len(lines) > 1 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// escapeSequence measures the escape sequence at the start of s, the way the
// kit's runner.StatusLine does. It reports its length, whether it moves the
// cursor to another line (so the text on either side is not one line), and,
// for a character set designation of G0, the set it selects.
func escapeSequence(s string) (n int, lineBreak bool, charset byte) {
	if len(s) < 2 {
		return len(s), false, 0
	}
	switch s[1] {
	case '[': // CSI: parameters and intermediates, then a final byte.
		for j := 2; j < len(s); j++ {
			if c := s[j]; c >= 0x40 && c <= 0x7e {
				return j + 1, strings.IndexByte("ABEFHfdJ", c) >= 0, 0
			}
		}
		return len(s), false, 0
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: up to BEL or ST.
		for j := 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1, false, 0
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2, false, 0
			}
		}
		return len(s), false, 0
	case '(', ')', '*', '+': // Character set designation: one more byte.
		if len(s) < 3 {
			return len(s), false, 0
		}
		if s[1] == '(' {
			return 3, false, s[2]
		}
		return 3, false, 0
	case 'E', 'D', 'M': // NEL, IND, RI: a new line.
		return 2, true, 0
	default: // ESC 7, ESC 8, ESC =, ESC > and the like.
		return 2, false, 0
	}
}
