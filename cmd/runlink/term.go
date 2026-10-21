package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// style is what the owner's terminal can show: cursor control for prompts and
// a status redrawn in place, color unless NO_COLOR is set, and Unicode symbols
// in a UTF-8 locale. Without cursor control, output is plain lines.
type style struct {
	live, color, unicode bool
	colors               int // 256, or 1<<24 for true color; fewer means the basic eight
}

func newStyle(getenv func(string) string, terminal bool) style {
	term := getenv("TERM")
	s := style{live: terminal && term != "" && term != "dumb"}
	s.color = s.live && getenv("NO_COLOR") == ""
	locale := strings.ToLower(cmp.Or(getenv("LC_ALL"), getenv("LC_CTYPE"), getenv("LANG")))
	s.unicode = strings.Contains(locale, "utf-8") || strings.Contains(locale, "utf8")
	switch {
	case getenv("COLORTERM") == "truecolor" || getenv("COLORTERM") == "24bit":
		s.colors = 1 << 24
	case strings.Contains(term, "256"):
		s.colors = 256
	}
	return s
}

// SGR codes for the few styles the CLI uses: state colors and emphasis.
const (
	bold   = "1"
	dim    = "2"
	red    = "31"
	green  = "32"
	yellow = "33"
)

func (s style) sgr(code, text string) string {
	if !s.color || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// sym picks a symbol's Unicode or ASCII form.
func (s style) sym(unicode, ascii string) string {
	if s.unicode {
		return unicode
	}
	return ascii
}

// foil is the recipient page's nacre foil, its only color: teal, blue, violet,
// rose, and gold.
var foil = [...][3]float64{{0x17, 0x9a, 0x8b}, {0x3b, 0x6c, 0xdc}, {0x78, 0x53, 0xdc}, {0xc0, 0x4b, 0x8a}, {0xad, 0x7b, 0x1d}}

// foilCode is the SGR color at position t along the foil, which repeats every 1.
func (s style) foilCode(t float64) string {
	x := (t - math.Floor(t)) * float64(len(foil))
	i := int(x)
	a, b, f := foil[i], foil[(i+1)%len(foil)], x-float64(i)
	var c [3]int
	for k := range c {
		c[k] = int(a[k] + (b[k]-a[k])*f)
	}
	switch {
	case s.colors >= 1<<24:
		return fmt.Sprintf("38;2;%d;%d;%d", c[0], c[1], c[2])
	case s.colors >= 256:
		cube := func(v int) int { return (v*5 + 127) / 255 }
		return fmt.Sprintf("38;5;%d", 16+36*cube(c[0])+6*cube(c[1])+cube(c[2]))
	}
	return "36"
}

// tint sets text in bold along the foil, from teal to gold.
func (s style) tint(text string) string {
	if !s.color {
		return text
	}
	runes := []rune(text)
	var b strings.Builder
	last := ""
	for i, r := range runes {
		if code := s.foilCode(0.8 * float64(i) / float64(len(runes))); code != last {
			b.WriteString("\x1b[1;" + code + "m")
			last = code
		}
		b.WriteRune(r)
	}
	return b.String() + "\x1b[0m"
}

// escape shows control characters as \x.. escapes, so text from a script or a
// file cannot drive the owner's terminal or fake a line.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) && r != '\t' {
			fmt.Fprintf(&b, "\\x%02x", r)
		} else {
			b.WriteRune(r) // invalid UTF-8 arrives as U+FFFD
		}
	}
	return b.String()
}

// runeCols is how many columns r takes, erring wide so a line never wraps.
func runeCols(r rune) int {
	switch {
	case r == '\t':
		return 8
	case r >= 0x1100 && (r < 0x2000 || r > 0x2bff):
		// ponytail: East Asian and emoji ranges count double, the symbols
		// between single; golang.org/x/text/width if that misjudges real output.
		return 2
	}
	return 1
}

// cols is the width of text, which may carry SGR styles, in columns.
func cols(text string) int {
	n := 0
	for i := 0; i < len(text); {
		if text[i] == 0x1b {
			i += max(strings.IndexByte(text[i:], 'm'), 0) + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		n += runeCols(r)
		i += size
	}
	return n
}

// fit cuts a line to n columns, keeping its styles, so it cannot wrap.
func fit(line string, n int, st style) string {
	if cols(line) <= n {
		return line
	}
	ellipsis := st.sym("…", "~")
	w := 0
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			i += max(strings.IndexByte(line[i:], 'm'), 0) + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if w += runeCols(r); w > n-1 {
			if st.color {
				return line[:i] + "\x1b[0m" + ellipsis
			}
			return line[:i] + ellipsis
		}
		i += size
	}
	return line
}

// pad fills text with spaces to n columns.
func pad(text string, n int) string {
	return text + strings.Repeat(" ", max(n-cols(text), 0))
}

// rawMode makes the terminal deliver each key as it is typed and unechoed,
// with Ctrl-C and Ctrl-S as plain keys; restore returns it to how it was.
func rawMode(f *os.File) (restore func(), err error) {
	fd := int(f.Fd())
	saved, err := unix.IoctlGetTermios(fd, getTermios)
	if err != nil {
		return nil, err
	}
	raw := *saved
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Iflag &^= unix.IXON
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, setTermios, &raw); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, setTermios, saved) }, nil
}

// width is the terminal's width in columns, or 80 when it does not say.
func width(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if size, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ); err == nil && size.Col > 0 {
			return int(size.Col)
		}
	}
	return 80
}

// region is the bottom of the terminal, redrawn in place: the prompts while the
// owner answers, then the status while sharing. Lines written above it stay.
type region struct {
	w     io.Writer
	st    style
	width func() int
	row   int // the cursor's line, counted from the region's first
}

// draw writes done above the region for good, replaces the region with lines,
// and leaves the cursor at column col of line at, or hidden below when at < 0.
// ponytail: a region taller than the terminal, or a narrowing terminal that
// rewraps it, redraws out of place; keep regions to a few lines.
func (r *region) draw(done, lines []string, at, col int) {
	var b strings.Builder
	b.WriteString("\r")
	if r.row > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", r.row)
	}
	b.WriteString("\x1b[J")
	for _, line := range done {
		b.WriteString(line + "\n")
	}
	n := r.width() - 1
	for _, line := range lines {
		b.WriteString(fit(line, n, r.st) + "\n")
	}
	if r.row = len(lines); at < 0 {
		b.WriteString("\x1b[?25l")
	} else {
		fmt.Fprintf(&b, "\x1b[%dA\x1b[%dG\x1b[?25h", len(lines)-at, col+1)
		r.row = at
	}
	_, _ = io.WriteString(r.w, b.String())
}

type keystroke struct {
	kind int
	r    rune
}

const (
	keyRune = iota
	keyEnter
	keyBack
	keyClear // Ctrl-U
	keyUp
	keyDown
	keyRight
	keyLeft
	keyCancel // Ctrl-C, or the input or the context ended
	keyOther
)

// keys reads the owner's keystrokes from a terminal in raw mode.
type keys struct{ in chan byte }

func readKeys(r io.Reader) *keys {
	k := &keys{make(chan byte, 4096)}
	go func() {
		defer close(k.in)
		buf := make([]byte, 512)
		for {
			n, err := r.Read(buf)
			for _, b := range buf[:n] {
				k.in <- b
			}
			if err != nil {
				return
			}
		}
	}()
	return k
}

// byte returns the next input byte, unless ctx or limit ends first.
func (k *keys) byte(ctx context.Context, limit <-chan time.Time) (byte, bool) {
	select {
	case b, ok := <-k.in:
		return b, ok
	case <-ctx.Done():
	case <-limit:
	}
	return 0, false
}

func (k *keys) next(ctx context.Context) keystroke {
	b, ok := k.byte(ctx, nil)
	switch {
	case !ok || b == 3:
		return keystroke{kind: keyCancel}
	case b == '\r' || b == '\n':
		return keystroke{kind: keyEnter}
	case b == 0x7f || b == 8:
		return keystroke{kind: keyBack}
	case b == 0x15:
		return keystroke{kind: keyClear}
	case b == 0x1b:
		// An arrow is Escape, [ or O, and a final letter; other sequences and
		// a lone Escape do nothing.
		limit := time.After(50 * time.Millisecond)
		if b, ok = k.byte(ctx, limit); !ok || b != '[' && b != 'O' {
			return keystroke{kind: keyOther}
		}
		for b, ok = k.byte(ctx, limit); ok && (b < 0x40 || b > 0x7e); b, ok = k.byte(ctx, limit) {
		}
		if i := strings.IndexByte("ABCD", b); ok && i >= 0 {
			return keystroke{kind: keyUp + i}
		}
		return keystroke{kind: keyOther}
	case b < 0x20:
		return keystroke{kind: keyOther}
	}
	p := []byte{b}
	for limit := time.After(50 * time.Millisecond); !utf8.FullRune(p); {
		b, ok := k.byte(ctx, limit)
		if !ok {
			break
		}
		p = append(p, b)
	}
	if r, _ := utf8.DecodeRune(p); r != utf8.RuneError && unicode.IsPrint(r) {
		return keystroke{kind: keyRune, r: r}
	}
	return keystroke{kind: keyOther}
}

var errCanceled = errors.New("canceled")

// asker asks the owner questions at the bottom of the terminal. Each answer
// stays above as one line; while hold is set, answers wait in held, still
// redrawn, until a recap replaces them.
type asker struct {
	ctx  context.Context
	keys *keys
	st   style
	r    *region
	held []string
	hold bool
}

// Questions and answers line up: a marker, the label in this many columns, the value.
const labelCols = 14

func (a *asker) prompt(label string) string {
	return a.st.sgr(a.st.foilCode(0), a.st.sym("❯", ">")) + " " + pad(label, labelCols)
}

// answer shows an answered question above the next one.
func (a *asker) answer(label, value string) {
	line := "  " + a.st.sgr(dim, pad(label, labelCols)) + value
	if a.hold {
		a.held = append(a.held, line)
		return
	}
	a.r.draw([]string{line}, nil, -1, 0)
}

// show commits lines above the prompts.
func (a *asker) show(lines ...string) { a.r.draw(lines, nil, -1, 0) }

// frame draws the current question below any held answers.
func (a *asker) frame(lines []string, at, col int) {
	if at >= 0 {
		at += len(a.held)
	}
	a.r.draw(nil, append(slices.Clone(a.held), lines...), at, col)
}

// note is the line under a question: its hint, or what is wrong with the answer.
func (a *asker) note(hint, problem string) []string {
	indent := strings.Repeat(" ", 2+labelCols)
	switch {
	case problem != "":
		return []string{indent + a.st.sgr(red, a.st.sym("✗ ", "x ")+problem)}
	case hint != "":
		return []string{indent + a.st.sgr(dim, hint)}
	}
	return nil
}

// text asks for one line. With color, the suggestion shows dimmed: typing
// replaces it, Backspace clears it, Right keeps it for editing, and Enter takes
// it. Without color it is ordinary text. check says what is wrong with an
// answer, shown until the owner edits it.
func (a *asker) text(label, suggestion, hint string, check func(string) string) (string, error) {
	return a.line(label, suggestion, hint, check, true)
}

// line is text, with the answer's surrounding spaces kept unless trim is set.
func (a *asker) line(label, suggestion, hint string, check func(string) string, trim bool) (string, error) {
	value, fresh, problem := []rune(suggestion), a.st.color && suggestion != "", ""
	for {
		shown := escape(string(value)) // a stored answer may hold control characters
		if room := a.r.width() - 3 - 2 - labelCols; cols(shown) > room {
			for shown != "" && cols(shown) > room-1 {
				_, size := utf8.DecodeRuneInString(shown)
				shown = shown[size:]
			}
			shown = a.st.sym("…", "~") + shown
		}
		col := 2 + labelCols + cols(shown)
		if fresh {
			shown = a.st.sgr(dim, shown)
		}
		a.frame(append([]string{a.prompt(label) + shown}, a.note(hint, problem)...), 0, col)
		switch k := a.keys.next(a.ctx); k.kind {
		case keyCancel:
			return "", errCanceled
		case keyEnter:
			answer := string(value)
			if trim {
				answer = strings.TrimSpace(answer)
			}
			if string(value) == suggestion {
				answer = suggestion // untouched, so exactly as it was
			}
			if check != nil {
				if problem = check(answer); problem != "" {
					continue
				}
			}
			a.answer(label, cmp.Or(escape(answer), a.st.sgr(dim, a.st.sym("–", "-"))))
			return answer, nil
		case keyRune:
			if fresh {
				value = nil
			}
			value, fresh, problem = append(value, k.r), false, ""
		case keyBack:
			if fresh {
				value = nil
			} else if len(value) > 0 {
				value = value[:len(value)-1]
			}
			fresh, problem = false, ""
		case keyClear:
			value, fresh, problem = nil, false, ""
		case keyRight:
			fresh = false
		}
	}
}

type option struct{ name, about string }

// choose asks for one of options, picked with the arrow keys or an option's
// first letter, starting at at.
func (a *asker) choose(label string, options []option, at int) (int, error) {
	for {
		lines := []string{a.prompt(label) + a.st.sgr(dim, a.st.sym("↑↓ and Enter", "arrows and Enter"))}
		for i, o := range options {
			mark, name := "  ", pad(o.name, 11)
			if i == at {
				mark, name = a.st.sgr(a.st.foilCode(0), a.st.sym("❯", ">"))+" ", a.st.sgr(bold, name)
			}
			lines = append(lines, strings.Repeat(" ", labelCols)+mark+name+" "+a.st.sgr(dim, o.about))
		}
		a.frame(lines, -1, 0)
		switch k := a.keys.next(a.ctx); k.kind {
		case keyCancel:
			return 0, errCanceled
		case keyEnter:
			a.answer(label, options[at].name)
			return at, nil
		case keyUp, keyLeft:
			at = (at + len(options) - 1) % len(options)
		case keyDown, keyRight:
			at = (at + 1) % len(options)
		case keyRune:
			for i := 1; i < len(options); i++ {
				if j := (at + i) % len(options); strings.HasPrefix(options[j].name, string(unicode.ToLower(k.r))) {
					at = j
					break
				}
			}
		}
	}
}

// confirm asks a yes-or-no question: y and n answer it, the arrows switch, and
// Enter takes the current answer.
func (a *asker) confirm(label, question string, yes bool) (bool, error) {
	for {
		pick := func(on bool, text string) string {
			if on {
				return a.st.sgr(a.st.foilCode(0), a.st.sym("●", "(*)")) + " " + a.st.sgr(bold, text)
			}
			return a.st.sgr(dim, a.st.sym("○", "( )")+" "+text)
		}
		line := a.prompt(label)
		if question != "" {
			line += question + "  "
		}
		a.frame([]string{line + pick(yes, "Yes") + "   " + pick(!yes, "No")}, -1, 0)
		switch k := a.keys.next(a.ctx); {
		case k.kind == keyCancel:
			return false, errCanceled
		case k.kind == keyRune && unicode.ToLower(k.r) == 'y':
			yes = true
		case k.kind == keyRune && unicode.ToLower(k.r) == 'n':
			yes = false
		case k.kind >= keyUp && k.kind <= keyLeft:
			yes = !yes
			continue
		case k.kind != keyEnter:
			continue
		}
		answer := "No"
		if yes {
			answer = "Yes"
		}
		if a.hold && question != "" { // held until a recap, so the question still reads
			answer = question + "  " + answer
		}
		a.answer(label, answer)
		return yes, nil
	}
}
