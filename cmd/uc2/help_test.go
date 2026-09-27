package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/uc2/cmd/uc2/internal/term"
)

// fakeKeys plays a script of key presses. A term.Key is a read of its own;
// a string or a []term.Key holds the keys of one read, the rest of which
// Flush drops. A func() runs when it is reached, to look at the screen or
// to resize it.
type fakeKeys struct {
	script []any
	read   []term.Key
	eofs   int
}

func (f *fakeKeys) Key() (term.Key, error) {
	for len(f.read) == 0 {
		if len(f.script) == 0 {
			if f.eofs++; f.eofs > 10 {
				panic("reading on at the end of the input")
			}
			return term.KeyNone, io.EOF
		}
		switch x := f.script[0].(type) {
		case term.Key:
			f.read = []term.Key{x}
		case []term.Key:
			f.read = x
		case string:
			for _, r := range x {
				f.read = append(f.read, term.Key(r))
			}
		case func():
			x()
		}
		f.script = f.script[1:]
	}
	k := f.read[0]
	f.read = f.read[1:]
	return k, nil
}

func (f *fakeKeys) Flush() { f.read = nil }

// helpApp returns an app on an emulated terminal of w x h characters that
// reads the keys of script. Colors follow NO_COLOR.
func helpApp(t *testing.T, w, h int, script ...any) (*app, *vt) {
	t.Setenv("TERM", "xterm")
	v := newVT(w, h)
	a := newApp(strings.NewReader(""), v, new(bytes.Buffer))
	a.inTTY, a.termOut = true, true
	a.sleep = func(time.Duration) {}
	a.openTUI = func() (*tui, error) {
		color := colorOn("auto", true)
		a.out.color = color
		v.raw = true
		return &tui{out: v, keys: &fakeKeys{script: script}, size: func() (int, int) { return v.w, v.h },
			w: v.w, h: v.h, color: color, restore: func() { v.raw = false }}, nil
	}
	return a, v
}

// runHelp runs uc2 with args on the terminal of a and checks the exit code
// and the terminal afterwards.
func runHelp(t *testing.T, a *app, v *vt, args ...string) {
	t.Helper()
	if code := a.run(args); code != 0 {
		t.Errorf("exit %d\n%s", code, a.stderr)
	}
	if len(v.bad) > 0 || v.raw || !v.modes["?7"] || !v.modes["?25"] || v.sgr != "" {
		t.Errorf("terminal left with errors %q, raw %v, modes %v, attributes %q", v.bad, v.raw, v.modes, v.sgr)
	}
}

// markups returns the screen with color markup.
func (v *vt) markups() []string {
	rows := make([]string, v.h)
	for y := range rows {
		rows[y] = strings.TrimRight(v.markup(y), " ")
	}
	return rows
}

// menuScreen is the help menu as UC2 revision 2 draws it (capture
// menu-r2-25.txt, snapshot 00; the escape codes are ANSI colors). The port
// has its own logo and texts, and no serial number line and configuration
// item, which makes it 4 rows shorter.
var menuScreen = []string{
	"{36}" + strings.Repeat("═", 75),
	"{36} ░███   ░███  ░████      {96}UltraCompressor II - Go - devel",
	"{36}░█  ░█   ░█   ░█  ░█",
	"{36}░█████   ░█   ░████",
	"{36}░█  ░█   ░█   ░█",
	"{36}░█  ░█  ░███  ░█",
	"{36}" + strings.Repeat("═", 75),
	"",
	"{93}HELP MENU {}(view{96} & search {}documentation)",
	"  {93} 0{}   -> WHATSNEW  what changed from UC2 2.3 to 2.4",
	"  {93} 1{}   -> README    how to get started, overview, features etc.",
	"  {93} 2{}   -> LICENSE   the license (LGPL), no warranty",
	"  {93} 3   -> BASIC     the most essential commands of UC",
	"  {93} 4{}   -> MAIN      the use of the UC command in detail",
	"  {93} 5{}   -> BBS       special features for BBS sysops",
	"  {93} 6{}   -> CONFIG    how to configure UC",
	"  {93} 7{}   -> BACKGRND  concepts, program design, compressor design, benchmarks",
	"  {93} 8{}   -> EXTEND    tools; extended commands and options",
	"",
	"{93}CHOICE (Escape to quit)?" + strings.Repeat(" ", 38) + "{}Mini help: {96}uc2 -?",
}

func TestHelpMenu(t *testing.T) {
	var v *vt
	var first []string
	var cursor [2]int
	var shown bool
	a, v := helpApp(t, 80, 25, func() {
		first, cursor, shown = v.markups(), [2]int{v.y, v.x}, v.modes["?25"]
	}, term.KeyEsc)
	var slept []time.Duration
	a.sleep = func(d time.Duration) { slept = append(slept, d) }
	runHelp(t, a, v)
	if !slices.Equal(first[:20], menuScreen) || strings.Join(first[20:], "") != "" {
		t.Errorf("menu:\n%s\nwant:\n%s", strings.Join(first, "\n"), strings.Join(menuScreen, "\n"))
	}
	if cursor != [2]int{19, 25} || !shown {
		t.Errorf("cursor at %v, shown %v", cursor, shown)
	}
	// Quitting leaves the menu, the echo and UC2's final message.
	want := append(slices.Clone(menuScreen[:19]),
		"{93}CHOICE (Escape to quit)? [Esc]"+strings.Repeat(" ", 32)+"{}Mini help: {96}uc2 -?", "", "{32}Everything went OK")
	if got := v.markups(); !slices.Equal(got[:22], want) || v.y != 22 || v.x != 0 {
		t.Errorf("final screen, cursor %d,%d:\n%s", v.y, v.x, strings.Join(got, "\n"))
	}
	if !slices.Equal(slept, []time.Duration{50 * time.Millisecond}) {
		t.Errorf("slept %v", slept)
	}
}

// highlight returns the highlighted item of the menu on the screen.
func highlight(v *vt) int {
	for i := range 9 {
		if !strings.Contains(v.markup(9+i), "{}") {
			return i
		}
	}
	return -1
}

func TestHelpMenuKeys(t *testing.T) {
	var v *vt
	var got []string
	hl := func() { got = append(got, fmt.Sprint(highlight(v))) }
	title := func() { got = append(got, v.line(0)) }
	a, v := helpApp(t, 80, 25,
		term.KeyDown, hl, term.KeyUp, term.KeyUp, term.KeyUp, term.KeyUp, term.KeyUp, hl, term.KeyDown, hl,
		"z", term.KeyLeft, "9", "c", "r", term.KeyOther, hl,
		term.KeyEnter, title, "q", hl, "7", title, "X", hl, "Q")
	var wrong []string
	a.sleep = func(d time.Duration) {
		if d == 500*time.Millisecond {
			wrong = append(wrong, v.line(19))
		}
	}
	runHelp(t, a, v)
	want := []string{"4", "8", "0", "0",
		" (WHATSNEW) 0. WHAT IS NEW (overview of changes from version 2.3 to 2.4)     18%", "0",
		" (BACKGRND) 7. UC2 TECHNICAL DETAILS (UC2 internals, design, benchmarks)      6%", "0"}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	prompt := func(echo string) string {
		return fmt.Sprintf("CHOICE (Escape to quit)? %-37sMini help: uc2 -?", echo+" *** WRONG CHOICE ***")
	}
	if w := []string{prompt("Z"), prompt("■"), prompt("9"), prompt("C"), prompt("R"), prompt("■")}; !slices.Equal(wrong, w) {
		t.Errorf("wrong choices:\n%s\nwant:\n%s", strings.Join(wrong, "\n"), strings.Join(w, "\n"))
	}
	if l := v.line(19); l != fmt.Sprintf("CHOICE (Escape to quit)? %-37sMini help: uc2 -?", "Q") {
		t.Errorf("after quitting %q", l)
	}

	// A wrong choice restores the prompt, not the whole menu; Ctrl-C quits.
	var kept bool
	a, v = helpApp(t, 80, 25, func() { v.cells[0][0].r = '#' }, "z", func() { kept = v.line(0)[0] == '#' }, term.KeyCtrlC)
	runHelp(t, a, v)
	if !kept || v.line(19) != fmt.Sprintf("CHOICE (Escape to quit)? %-37sMini help: uc2 -?", "■") || v.line(21) != "Everything went OK" {
		t.Errorf("menu redrawn %v, or not quit by Ctrl-C:\n%s", !kept, strings.Join(v.lines(), "\n"))
	}
}

func TestHelpWords(t *testing.T) {
	for _, tc := range []struct {
		words []string
		title string
		row6  string
	}{
		{[]string{"damage"}, "(README) 1.A WHAT IS ULTRA-COMPRESSOR? 8%",
			"{96;44}UC2 can also make such an archive {93;40}DAMAGE{96;44} PROTECTED (tm), so ALL files in"},
		{[]string{"damage", "protected"}, "(README) 1.A WHAT IS ULTRA-COMPRESSOR? 8%",
			"{96;44}UC2 can also make such an archive {93;40}DAMAGE PROTECTED{96;44} (tm), so ALL files in"},
		// Error numbers are looked up in 8.G, where 2.4 has its error list.
		{[]string{"105"}, "(EXTEND) 8.G ERROR MESSAGES 82%", "{96;44} {93;40}105{96;44}  Some problem with UC.EXE."},
		// "jump para" of UC2's mini help is a search too.
		{[]string{"1.E"}, "(README) 1.D FEATURES, BUSINESS/CORPORATE USE 54%", "{93;40}1.E{97;44} FEATURES, DEVELOPERS"},
	} {
		var v *vt
		var got []string
		a, v := helpApp(t, 80, 25, func() {
			got = append(got, strings.Join(strings.Fields(v.line(0)), " "), strings.TrimRight(v.markup(6), " "))
		}, term.KeyEsc, func() { got = append(got, fmt.Sprint(highlight(v))) }, term.KeyEsc)
		runHelp(t, a, v, append([]string{"-?"}, tc.words...)...)
		if want := []string{tc.title, tc.row6, "3"}; !slices.Equal(got, want) {
			t.Errorf("%q:\n%s\nwant:\n%s", tc.words, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
	for _, tc := range []struct {
		words []string
		want  string
	}{
		{[]string{"105"}, "8GS105\r"}, {[]string{"-3", "x"}, "8GS-3 X\r"}, {[]string{"1.E"}, "1S1.E\r"},
		{[]string{"0"}, "1S0\r"}, {[]string{"damage", "Protected"}, "1SDAMAGE PROTECTED\r"}, {[]string{"x1"}, "1SX1\r"},
	} {
		var got []rune
		for _, k := range searchKeys(tc.words) {
			got = append(got, rune(k))
		}
		if string(got) != tc.want {
			t.Errorf("%q: keys %q, want %q", tc.words, string(got), tc.want)
		}
	}
}

func TestHelpWordSlash(t *testing.T) {
	if isHelpWord("/home") != (runtime.GOOS == "windows") || !isHelpWord("-h") || !isHelpWord("hx/") {
		t.Error("/ is an option prefix only on Windows")
	}
}

func TestHelpFallback(t *testing.T) {
	for _, tc := range []struct {
		args               []string
		terminal, dumb, ok bool
		menu               bool
	}{
		{args: nil, terminal: false, ok: true},
		{args: []string{"--help"}, terminal: false, ok: true},
		{args: []string{"-?", "damage"}, terminal: false, ok: true},
		{args: []string{"-?"}, terminal: true, ok: true},
		{args: []string{"-h"}, terminal: true, ok: true},
		{args: []string{"help"}, terminal: true, ok: true},
		// UC2 takes any word starting with ?, h or H (after a - or /).
		{args: []string{"h"}, terminal: true, ok: true},
		{args: []string{"H"}, terminal: true, ok: true},
		{args: []string{"-?x"}, terminal: true, ok: true},
		{args: []string{"h", "x"}, terminal: true, ok: true, menu: true},
		{args: []string{"help", "x"}, terminal: true, ok: true, menu: true},
		{args: nil, terminal: true, dumb: true, ok: true},
		{args: nil, terminal: true, ok: false}, // raw mode failed or the window is too small
		{args: nil, terminal: true, ok: true, menu: true},
		{args: []string{"--help"}, terminal: true, ok: true},
		{args: []string{"-help"}, terminal: true, ok: true},
		{args: []string{"?", "x"}, terminal: true, ok: true, menu: true},
		{args: []string{"--help", "x"}, terminal: true, ok: true},
		{args: []string{"-HELP", "x"}, terminal: true, ok: true},
	} {
		a, v := helpApp(t, 80, 60, term.KeyEsc)
		a.inTTY = tc.terminal
		if tc.dumb {
			t.Setenv("TERM", "dumb")
		}
		open := a.openTUI
		opened := false
		a.openTUI = func() (*tui, error) {
			opened = true
			if !tc.ok {
				return nil, errors.New("no")
			}
			return open()
		}
		runHelp(t, a, v, tc.args...)
		out := strings.Join(v.lines(), "\n")
		if strings.Contains(out, "HELP MENU") != tc.menu || strings.Contains(out, "SYNTAX: uc2 command") == tc.menu || opened != (tc.menu || !tc.ok) {
			t.Errorf("%+v: opened %v:\n%s", tc, opened, out)
		}
	}
}

func TestHelpMono(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var v *vt
	var got []string
	row := func(y int) func() { return func() { got = append(got, strings.TrimRight(v.markup(y), " ")) } }
	a, v := helpApp(t, 80, 25, row(1), row(9), row(12), "3S", "damage\r", row(0), row(6), row(23), term.KeyEsc, term.KeyEsc)
	runHelp(t, a, v)
	want := []string{
		" ░███   ░███  ░████      {1}UltraCompressor II - Go - devel",
		"  {1} 0{}   -> WHATSNEW  what changed from UC2 2.3 to 2.4",
		"  {1} 3   -> BASIC     the most essential commands of UC",
		"{7} (BASIC) 3.B COMPRESSING A COMPLETE DIRECTORY (TREE) INTO AN ARCHIVE         78%",
		"3.C MAKING AN ARCHIVE {1}DAMAGE{} PROTECTED",
		"{1;7} Tab {7}summary & exit  {1;7}Esc {7}menu  {1;7}0-8 A-Z {7}jump  {1;7}↑ ↓ PgUp PgDn  S{7}earch",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if l := v.markup(21); l != "Everything went OK" {
		t.Errorf("final line %q", l)
	}
}

func TestViewFile(t *testing.T) {
	setup(t, map[string]string{"dos.txt": "", "a.txt": "1. X\n==\nA.\ttab\x1b[2J\f\r\n1.Z SUMMARY\n===\nsum\n"})
	os.WriteFile("dos.txt", []byte("caf\x82\r\nend\r\n\x1ajunk"), 0o666)
	if got := textLines([]byte("caf\x82\r\nend\r\n\x1ajunk")); !slices.Equal(got, []string{"café", "end"}) {
		t.Errorf("DOS text %q", got)
	}
	long := strings.Repeat("0123456789", 17)
	if got := textLines([]byte("\x00\x1f\x7f\r\u0085\n" + long)); !slices.Equal(got, []string{" ▼⌂♪?", long[:84], long[84:168], long[168:]}) {
		t.Errorf("controls and long lines %q", got)
	}
	// UC2 r2 showed an empty line after a line of 84 or 168 characters, but
	// not after an unterminated last one.
	x84, y168, z83, w85 := strings.Repeat("x", 84), strings.Repeat("y", 168), strings.Repeat("z", 83), strings.Repeat("w", 85)
	if got := textLines([]byte(x84 + "\n" + y168 + "\r\n" + z83 + "\n" + w85 + "\n" + x84)); !slices.Equal(got, []string{x84, "", y168[:84], y168[84:], "", z83, w85[:84], "w", x84}) {
		t.Errorf("lines of 84 characters %q", got)
	}
	if got := textLines(nil); len(got) != 0 {
		t.Errorf("empty file %q", got)
	}
	var v *vt
	var got []string
	a, v := helpApp(t, 80, 25, func() { got = append(got, v.markups()[:4]...); got = append(got, v.markups()[23]) }, "S1", term.KeyTab)
	if err := a.viewFile("a.txt"); err != nil {
		t.Fatal(err)
	}
	a.out.Flush()
	want := []string{
		fmt.Sprintf("{30;47} %-74s 100%%", "a.txt"),
		"{97;44}1. X",
		"{96;44}==",
		"{96;44}A.○tab←[2J",
		"{31;47} Tab {30;47}summary & exit  {31;47}Esc {30;47}exit  {31;47}A-Z {30;47}jump  {31;47}↑ ↓ PgUp PgDn  {30;47}",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Tab leaves the summary on the screen, and the cursor below it.
	if l := v.lines(); l[0] != "1.Z SUMMARY" || l[2] != "sum" || l[3] != "" || v.y != 3 || v.x != 0 || len(v.bad) > 0 || v.raw || !v.modes["?7"] {
		t.Errorf("after Tab, cursor %d,%d:\n%s", v.y, v.x, strings.Join(l, "\n"))
	}

	a, v = helpApp(t, 80, 25, term.KeyEsc)
	if err := a.viewFile("dos.txt"); err != nil || strings.Join(v.lines(), "") != "" {
		t.Errorf("after Esc: %v\n%s", err, strings.Join(v.lines(), "\n"))
	}
	for name, want := range map[string]string{"none.txt": "cannot locate file none.txt (for viewing)", ".": "cannot open file . (for viewing)"} {
		if err := a.viewFile(name); err == nil || err.Error() != want || code(err) != sevEditor {
			t.Errorf("%s: %v", name, err)
		}
	}
	a.inTTY = false
	if err := a.viewFile("a.txt"); code(err) != sevEditor {
		t.Errorf("without a terminal: %v", err)
	}
}

func TestViewFileAfterOutput(t *testing.T) {
	// Buffered output of an earlier command comes before the viewer, which
	// clears it away.
	setup(t, map[string]string{"V.TXT": "text\n"})
	writeArchive(t, "T.UC2", "I1.TXT")
	a, v := helpApp(t, 80, 25, term.KeyEsc)
	if code := a.run([]string{"~D", "T", "&", "~V", "V.TXT"}); code != 0 || strings.Join(v.lines(), "") != "" {
		t.Errorf("exit %d, screen after Esc:\n%s", code, strings.Join(v.lines(), "\n"))
	}
}

func TestHelpInterrupted(t *testing.T) {
	// An interrupt restores the terminal below the screen, as a panic does.
	var a *app
	var v *vt
	var got string
	a, v = helpApp(t, 80, 25, "0", func() {
		a.abort()
		got = fmt.Sprint(v.y, v.x, v.raw, v.modes, v.sgr)
	}, func() { panic("boom") })
	if code := a.run(nil); code != sevInternal || got != "24 0 false map[?25:true ?7:true]" {
		t.Errorf("exit %d, after the interrupt %s", code, got)
	}
	if e := a.stderr.(*bytes.Buffer).String(); !strings.Contains(e, "FATAL ERROR 100: program aborted by user") || !strings.Contains(e, "internal error: boom") {
		t.Errorf("stderr %q", e)
	}
	if v.raw || !v.modes["?7"] || !v.modes["?25"] {
		t.Errorf("after the panic: raw %v, modes %v", v.raw, v.modes)
	}
}
