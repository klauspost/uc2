package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/klauspost/uc2/cmd/uc2/internal/term"
	"github.com/klauspost/uc2/internal/charset"
)

const (
	curOn       = "\x1b[?25h"
	curOff      = "\x1b[?25l"
	wrapOn      = "\x1b[?7h"
	wrapOff     = "\x1b[?7l"
	clearScreen = "\x1b[0m\x1b[H\x1b[2J"
)

// The help needs UC2's 80 columns and the 20 rows of its menu.
const minW, minH = 80, 20

// keyReader reads the keys of the full-screen help.
type keyReader interface {
	Key() (term.Key, error)
	Flush()
}

// tui is the terminal of the full-screen help. As UC2, it draws on the
// main screen, with the cursor addressed and autowrap off, so that rows
// may fill the width.
type tui struct {
	out     io.Writer
	keys    keyReader
	size    func() (w, h int)
	w, h    int
	color   bool     // else UC2's monochrome look
	shown   []string // the rows of the viewer on screen, nil if unknown
	restore func()   // ends raw mode
	once    sync.Once
}

// openTTY opens the terminal for the full-screen help: keys from stdin in
// raw mode, VT sequences on stdout. It fails if the window is too small.
func (a *app) openTTY() (*tui, error) {
	in, ok1 := a.rawIn.(*os.File)
	out, ok2 := a.stdout.(*os.File)
	if !ok1 || !ok2 || !a.vtOut {
		return nil, errors.ErrUnsupported
	}
	ofd := int(out.Fd())
	t := &tui{out: out, color: a.out.color, size: func() (int, int) {
		w, h, err := term.GetSize(ofd)
		if err != nil || w < 1 || h < 1 {
			return 80, 25 // UC2's screen
		}
		return w, h
	}}
	if t.w, t.h = t.size(); t.small() {
		return nil, errors.New("window too small")
	}
	fd := int(in.Fd())
	st, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	resize, stop := term.NotifyResize(ofd)
	if a.keys == nil {
		a.keys = term.NewKeys(in)
	}
	a.keys.Resize, t.keys = resize, a.keys
	t.restore = func() {
		stop()
		term.Restore(fd, st)
	}
	return t, nil
}

// stdinReader reads standard input, through the key reader of the
// full-screen help once there is one: its goroutine takes the next input.
type stdinReader struct{ a *app }

func (r stdinReader) Read(p []byte) (int, error) {
	if r.a.keys != nil {
		return r.a.keys.Read(p)
	}
	return r.a.rawIn.Read(p)
}

// canMenu reports whether the full-screen help can run.
func (a *app) canMenu() bool {
	return a.inTTY && a.termOut && os.Getenv("TERM") != "dumb"
}

// session starts full-screen output on t and returns the function that
// ends it normally; an interrupt ends it too.
func (a *app) session(t *tui) (end func()) {
	a.breakLine() // the output of earlier commands goes first: t writes around a.out
	t.write(wrapOff)
	tempMu.Lock()
	a.unraw = func() { t.close(false) }
	tempMu.Unlock()
	return func() {
		tempMu.Lock()
		a.unraw = nil
		tempMu.Unlock()
		t.close(true)
	}
}

// close restores the terminal. When the help did not end normally (ok is
// false), the cursor first moves below the screen, for the messages that
// follow.
func (t *tui) close(ok bool) {
	t.once.Do(func() {
		if !ok {
			t.write(fmt.Sprintf("\x1b[%d;1H\r\n", t.h))
		}
		t.write("\x1b[0m" + wrapOn + curOn)
		if t.restore != nil {
			t.restore()
		}
	})
}

func (t *tui) write(s string) { io.WriteString(t.out, s) }

func (t *tui) small() bool { return t.w < minW || t.h < minH }

// page is the number of text rows of the viewer. Keys still act while the
// window is too small, so it stays positive even then.
func (t *tui) page() int { return max(t.h-3, 1) }

func (t *tui) clear() {
	t.write(clearScreen)
	t.shown = nil
}

// draw shows the rows of the viewer, rewriting only rows that changed.
func (t *tui) draw(rows []string) {
	var b strings.Builder
	switch {
	case t.small():
		if t.shown == nil {
			b.WriteString(clearScreen + curOff + fmt.Sprintf("The help needs %dx%d characters", minW, minH))
		}
		rows = []string{}
	default:
		b.WriteString(curOff)
		for y, r := range rows {
			if y >= len(t.shown) || t.shown[y] != r {
				fmt.Fprintf(&b, "\x1b[%d;1H%s", y+1, r)
			}
		}
		b.WriteString("\x1b[0m")
	}
	t.shown = rows
	t.write(b.String())
}

// Color codes of the menu as SGR; without colors, UC2's monochrome mode
// shows the highlights bright.
var (
	colorCodes = strings.NewReplacer(cT, sgr[3], cH, sgr[4], cOK, sgr[5], cAsk, sgr[6], cN, sgr[7], cErr, sgr[8])
	monoCodes  = strings.NewReplacer(cT, "\x1b[0;1m", cH, "\x1b[0m", cOK, "\x1b[0;1m", cAsk, "\x1b[0;1m", cN, "\x1b[0m", cErr, "\x1b[0;1m")
)

func (t *tui) paint(s string) string {
	if t.color {
		return colorCodes.Replace(s)
	}
	return monoCodes.Replace(s)
}

// attrs holds the SGR attributes of the viewer, which sets its colors
// directly (VIEWS.CPP).
type attrs struct{ hdr, key, txt, head, hit, busy, fail, ask string }

var (
	colorAttrs = attrs{hdr: "30;47", key: "31;47", txt: "96;44", head: "97;44", hit: "93;40", busy: "97", fail: "91", ask: "93"}
	monoAttrs  = attrs{hdr: "7", key: "1;7", txt: "0", head: "0", hit: "1", busy: "0", fail: "1", ask: "1"}
)

func (t *tui) attrs() *attrs {
	if t.color {
		return &colorAttrs
	}
	return &monoAttrs
}

// row renders a row of the full width from pairs of attribute and text;
// fill colors the rest.
func (t *tui) row(fill string, segs ...string) string {
	var b strings.Builder
	n := 0
	for i := 0; i < len(segs); i += 2 {
		if segs[i+1] != "" {
			b.WriteString("\x1b[0;" + segs[i] + "m" + segs[i+1])
			n += utf8.RuneCountInString(segs[i+1])
		}
	}
	b.WriteString("\x1b[0;" + fill + "m" + strings.Repeat(" ", max(t.w-n, 0)))
	return b.String()
}

// help is a session of UC2's help menu and document viewer (MAIN.CPP
// HelpMenu, VIEWS.CPP).
type help struct {
	a     *app
	t     *tui
	docs  []doc
	file  bool       // "UC ~V file": one document, no document keys, no search
	find  string     // the search text, upper case; UC2 keeps it for the run
	queue []term.Key // typed ahead by "uc2 -? words"
}

// keyEOF is the end of the input, which leaves the help.
const keyEOF term.Key = -100

// key returns the next key, with letters upper case as UC2's GetKey did.
func (h *help) key() term.Key {
	var k term.Key
	if len(h.queue) > 0 {
		k, h.queue = h.queue[0], h.queue[1:]
	} else if key, err := h.t.keys.Key(); err != nil {
		return keyEOF
	} else {
		k = key
	}
	if k == term.KeyResize {
		h.t.w, h.t.h = h.t.size()
		h.t.clear()
	}
	if k >= 'a' && k <= 'z' {
		k -= 'a' - 'A'
	}
	return k
}

// viewer is the position in the documents and the message row.
type viewer struct {
	cur, ofs  int // document and its line at the top of the page
	msg, attr string
}

// view shows document d (VIEWS.CPP ViewFile, ViewIt) until Esc, X or Q
// return to the menu, and reports whether Tab showed the summary instead,
// which ends the program.
func (h *help) view(d int) bool {
	t := h.t
	v := &viewer{cur: d}
	t.clear()
	failed := false
	for {
		t.draw(h.frame(v))
		k := h.key()
		if failed {
			v.msg, failed = "", false
		}
		lines, page := h.docs[v.cur].lines, t.page()
		down := func(n int) {
			if len(lines) > page && v.ofs < len(lines)-page {
				v.ofs = min(v.ofs+n, len(lines)-page)
			}
		}
		switch {
		case k == 'S' && !h.file && !t.small():
			failed = h.search(v)
		case k >= 'A' && k <= 'L' || k == 'Z':
			if i := anchor(lines, byte(k)); i >= 0 {
				v.ofs = i // not clamped: the page may end below the text
			}
		case k >= '0' && k <= '8' && !h.file:
			v.cur, v.ofs = int(k-'0'), 0
		case k == term.KeyEsc || k == 'X' || k == 'Q' || k == term.KeyCtrlC || k == keyEOF:
			t.clear()
			return false
		case k == term.KeyTab:
			h.summary(lines)
			return true
		case k == term.KeyUp:
			v.ofs = max(v.ofs-1, 0)
			t.keys.Flush() // no overshoot from key repeat
		case k == term.KeyPgUp:
			v.ofs = max(v.ofs-page, 0)
			t.keys.Flush()
		case k == term.KeyDown:
			down(1)
			t.keys.Flush()
		case k == term.KeyPgDn || k == ' ':
			down(page)
			t.keys.Flush()
		case k == term.KeyHome:
			v.ofs = 0
		case k == term.KeyEnd:
			down(len(lines))
		case k == term.KeyResize:
			v.ofs = min(v.ofs, max(len(lines)-page, 0))
		}
	}
}

// frame renders the viewer (VIEWS.CPP:331-484): title bar, text, status
// bar and message row. It keeps UC2's layout of 80 columns, but paints the
// rows across the full width.
func (h *help) frame(v *viewer) []string {
	t, a, d := h.t, h.t.attrs(), &h.docs[v.cur]
	page := t.page()
	title := d.name
	if !h.file {
		title = "(" + d.name + ") " + heading(d.lines, v.ofs, byte('0'+v.cur))
	}
	pct := 100
	if len(d.lines) > 0 {
		pct = min(100, 100*(v.ofs+page)/len(d.lines))
	}
	rows := []string{t.row(a.hdr, a.hdr, fmt.Sprintf(" %s %3d%%", fit(title, 74), pct))}
	for i := v.ofs; i < v.ofs+page; i++ {
		if i < len(d.lines) {
			rows = append(rows, h.textRow(d.lines[i]))
		} else {
			rows = append(rows, t.row(a.txt))
		}
	}
	esc, jump := "menu  ", "0-8 A-Z "
	if h.file {
		esc, jump = "exit  ", "A-Z "
	}
	segs := []string{a.key, " Tab ", a.hdr, "summary & exit  ", a.key, "Esc ", a.hdr, esc, a.key, jump, a.hdr, "jump  ", a.key, "↑ ↓ PgUp PgDn  "}
	if !h.file {
		segs = append(segs, a.key, "S", a.hdr, "earch")
	}
	return append(rows, t.row(a.hdr, segs...), t.row("0", v.attr, cut(v.msg, t.w)))
}

// textRow renders a line (VIEWS.CPP writeline): cut or padded to 80
// columns, headings in white, and each occurrence of the search text in
// yellow on black.
func (h *help) textRow(line string) string {
	a := h.t.attrs()
	s := fit(line, 80)
	base := a.txt
	if isHeading(s) {
		base = a.head
	}
	var segs []string
	if h.find != "" {
		up := asciiUpper(s)
		for i := strings.Index(up, h.find); i >= 0; i = strings.Index(up, h.find) {
			j := i + len(h.find)
			segs = append(segs, base, s[:i], a.hit, s[i:j])
			s, up = s[j:], up[j:]
		}
	}
	return h.t.row(base, append(segs, base, s)...)
}

// isHeading reports whether a line padded to 80 columns is shown as a
// heading: "N. ", "N.X" or "===".
func isHeading(s string) bool {
	return s[0] >= '0' && s[0] <= '9' && s[1] == '.' && (s[2] >= 'A' && s[2] <= 'Z' || s[2] == ' ') || strings.HasPrefix(s, "===")
}

// fit cuts or pads s to n columns.
func fit(s string, n int) string {
	s = cut(s, n)
	return s + strings.Repeat(" ", n-utf8.RuneCountInString(s))
}

// cut cuts s to at most n columns.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// asciiUpper upper-cases like strupr, keeping the byte offsets.
func asciiUpper(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r - 'a' + 'A'
		}
		return r
	}, s)
}

// para reports whether line i is a heading "N.X ..." underlined with '='.
func para(lines []string, i int) bool {
	return len(lines[i]) > 1 && lines[i][1] == '.' && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "==")
}

// anchor returns the line of paragraph c, or -1.
func anchor(lines []string, c byte) int {
	for i := range lines {
		if para(lines, i) && len(lines[i]) > 2 && lines[i][2] == c {
			return i
		}
	}
	return -1
}

// heading returns the title of the page at line ofs: the nearest heading
// of chapter digit at or above it, else the first line.
func heading(lines []string, ofs int, digit byte) string {
	for i := ofs; i > 0; i-- {
		if para(lines, i) && lines[i][0] == digit {
			return lines[i]
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

// search asks for the search text and looks for it (VIEWS.CPP:513-546).
// It reports whether the search failed, which shows a message until the
// next key.
func (h *help) search(v *viewer) bool {
	a := h.t.attrs()
	s := h.ask(v)
	v.msg, v.attr = "Searching...", a.busy
	h.t.draw(h.frame(v))
	h.find, v.msg = s, ""
	if h.find == "" || h.next(v) {
		return false
	}
	v.msg, v.attr = `Could not find "`+h.find+`"`, a.fail
	return true
}

// ask reads the search text on the message row (MAIN.CPP ask): it starts
// with the previous text, Esc clears it and Enter accepts it.
func (h *help) ask(v *viewer) string {
	const q = "Search all documentation for : "
	t, s := h.t, h.find
	for {
		shown := s
		if n := len(q) + len(s) - (t.w - 1); n > 0 {
			shown = s[min(n, len(s)):] // the cursor stays on the row
		}
		v.msg, v.attr = q+shown, t.attrs().ask
		t.draw(h.frame(v))
		if !t.small() {
			t.write(fmt.Sprintf("\x1b[%d;%dH", t.h, len(q)+len(shown)+1) + curOn)
		}
		switch k := h.key(); {
		case k == term.KeyEnter:
			return s
		case k == keyEOF:
			return ""
		case k == term.KeyBack:
			s = s[:max(len(s)-1, 0)]
		case k == term.KeyEsc || k == term.KeyCtrlC:
			s = ""
		case k >= ' ' && k < 0x7f:
			s = s[:min(len(s), 79)] + string(rune(k))
		}
	}
}

// next moves to the next line with the search text, starting below the
// page and going through the following documents, around to this one
// again (VIEWS.CPP Search). The line ends up sixth on the page, if the
// document is long enough.
func (h *help) next(v *viewer) bool {
	page := h.t.page()
	d, i := v.cur, v.ofs+page
	for swaps := 10; swaps > 0; i++ {
		if i >= len(h.docs[d].lines) {
			d, i = (d+1)%len(h.docs), 0
			swaps--
		}
		if lines := h.docs[d].lines; i < len(lines) && strings.Contains(asciiUpper(lines[i]), h.find) {
			v.cur, v.ofs = d, min(max(i-5, 0), max(len(lines)-page, 0))
			return true
		}
	}
	return false
}

// summary prints the document's Z paragraph from the top of the cleared
// screen, where it stays (VIEWS.CPP:592-622). The cursor waits at the
// start of its last line.
func (h *help) summary(lines []string) {
	var b strings.Builder
	b.WriteString(clearScreen)
	if z := anchor(lines, 'Z'); z > 0 {
		for i, l := range lines[z:] {
			if i > 0 {
				b.WriteString("\x1b[0m\r\n")
			}
			b.WriteString(h.textRow(l))
		}
		b.WriteString("\x1b[0m\r")
	}
	h.t.write(b.String())
	h.t.shown = nil
}

// viewFile shows a text file in the viewer, as "UC ~V file" does: the
// title is the file name, and there are no document keys and no search.
// Esc clears the screen; Tab leaves the file's Z paragraph on it.
func (a *app) viewFile(name string) error {
	b, err := os.ReadFile(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fatalf(sevEditor, "cannot locate file %s (for viewing)", name)
	case err != nil:
		return fatalf(sevEditor, "cannot open file %s (for viewing)", name)
	case !a.canMenu():
		return fatalf(sevEditor, "cannot view %s (not a terminal)", name)
	}
	t, err := a.openTUI()
	if err != nil {
		return fatalf(sevEditor, "cannot view %s (%v)", name, err)
	}
	end := a.session(t)
	defer t.close(false)
	h := &help{a: a, t: t, docs: []doc{{clean(name), textLines(b)}}, file: true}
	tab := h.view(0)
	end()
	if tab {
		fmt.Fprint(a.out, "\n")
	}
	return nil
}

// controlGlyphs are the PC's glyphs of the control characters, which UC2's
// viewer showed; they also keep escape sequences away from the terminal.
var controlGlyphs = []rune(" ☺☻♥♦♣♠•◘○◙♂♀♪♫☼►◄↕‼¶§▬↨↑↓→←∟↔▲▼")

// textLines splits a file for the viewer. It is UTF-8 or else DOS text,
// which ends at Ctrl-Z. As in UC2's viewer, form feeds are removed, other
// control characters shown as glyphs (a tab as '○') and lines split after
// 84 characters. UC2 read them with fgets(line, 85, f), which also leaves an
// empty line after a line of 84, 168, ... characters.
func textLines(b []byte) []string {
	s := string(b)
	if !utf8.Valid(b) {
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = charset.CP437.DecodeByte(c)
		}
		s = string(r)
	}
	s, _, _ = strings.Cut(s, "\x1a")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var lines []string
	for s != "" {
		l, rest, nl := strings.Cut(s, "\n")
		s = rest
		r := []rune(strings.Map(func(r rune) rune {
			switch {
			case r == '\f':
				return -1
			case r < 0x20:
				return controlGlyphs[r]
			case r == 0x7f:
				return '⌂'
			}
			return r
		}, l))
		for ; len(r) >= 84; r = r[84:] {
			lines = append(lines, clean(string(r[:84])))
		}
		if nl || len(r) > 0 {
			lines = append(lines, clean(string(r)))
		}
	}
	return lines
}
