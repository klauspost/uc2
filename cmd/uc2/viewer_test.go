package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/uc2/cmd/uc2/internal/term"
)

// titles records the title bar with its spaces collapsed.
func titles(v **vt, got *[]string) func() {
	return func() { *got = append(*got, strings.Join(strings.Fields((*v).line(0)), " ")) }
}

func TestViewer(t *testing.T) {
	var v *vt
	var got []string
	title := titles(&v, &got)
	var page []string
	a, v := helpApp(t, 80, 25,
		"0", title, func() { page = v.markups() }, term.KeyPgDn, title, term.KeyPgDn, term.KeyPgDn, title, "c", title,
		"1", title, "j", title, "2", title, "8", title, "g", title, term.KeyEnd, title, term.KeyHome, title, term.KeyUp, title,
		[]term.Key{term.KeyPgDn, term.KeyPgDn}, title, term.KeyDown, title, term.KeyUp, title, " ", title,
		"3", "z", title, func() {
			// Not clamped: the rest of the page is blue.
			if v.line(16) != "" || v.attr(16, 0, 80) != "96;44" || v.line(1) != "3.Z SUMMARY" {
				t.Errorf("after Z:\n%s", strings.Join(v.lines(), "\n"))
			}
		}, term.KeyEnd, title, term.KeyUp, title, "k", title, "\r", term.KeyLeft, term.KeyRight, "9", "m", title,
		term.KeyEsc, term.KeyEsc)
	runHelp(t, a, v)
	want := []string{
		// Chapters 0, 3 and 8 as UC2 revision 2 showed them (captures
		// menu-r2-m24*.txt); chapter 1 is 2 lines shorter, and 2 is new.
		"(WHATSNEW) 0. WHAT IS NEW (overview of changes from version 2.3 to 2.4) 18%",
		"(WHATSNEW) 0.A BUG FIXES 36%",
		"(WHATSNEW) 0.B ENHANCEMENTS 73%",
		"(WHATSNEW) 0.C KNOWN PROBLEMS/ISSUES 91%",
		"(README) 1. INTRODUCTION (how to get started, features, etc.) 3%",
		"(README) 1.J PRESS RELEASE 87%",
		"(LICENSE) 2. LICENSE (the license agreement, warranty, etc.) 40%",
		"(EXTEND) 8. UC2 EXTENDED COMMANDS 2%",
		"(EXTEND) 8.G ERROR MESSAGES 69%",
		"(EXTEND) 8.Z SUMMARY 100%",
		"(EXTEND) 8. UC2 EXTENDED COMMANDS 2%",
		"(EXTEND) 8. UC2 EXTENDED COMMANDS 2%",
		"(EXTEND) 8. UC2 EXTENDED COMMANDS 5%", // key repeat does not overshoot
		"(EXTEND) 8. UC2 EXTENDED COMMANDS 6%",
		"(EXTEND) 8. UC2 EXTENDED COMMANDS 5%",
		"(EXTEND) 8. UC2 EXTENDED COMMANDS 8%",
		"(BASIC) 3.Z SUMMARY 100%",
		"(BASIC) 3.Z SUMMARY 100%",
		"(BASIC) 3.D VERBOSE LIST OF THE ARCHIVE 100%",
		"(BASIC) 3.D VERBOSE LIST OF THE ARCHIVE 100%",
		"(BASIC) 3.D VERBOSE LIST OF THE ARCHIVE 100%",
	}
	if !slices.Equal(got, want) {
		t.Errorf("titles:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i, l := range manual()[0].lines[:22] {
		attr := "96;44"
		if isHeading(fit(l, 80)) {
			attr = "97;44"
		}
		if want := "{" + attr + "}" + l; page[i+1] != want {
			t.Errorf("row %d: %q, want %q", i+1, page[i+1], want)
		}
	}
	if s := "{30;47} (WHATSNEW) 0. WHAT IS NEW (overview of changes from version 2.3 to 2.4)     18%"; page[0] != s {
		t.Errorf("title bar %q", page[0])
	}
	if s := "{31;47} Tab {30;47}summary & exit  {31;47}Esc {30;47}menu  {31;47}0-8 A-Z {30;47}jump  {31;47}↑ ↓ PgUp PgDn  S{30;47}earch"; page[23] != s || page[24] != "" {
		t.Errorf("status bar %q, message row %q", page[23], page[24])
	}
}

func TestViewerSearch(t *testing.T) {
	var v *vt
	var got []string
	title := titles(&v, &got)
	row := func(y int) func() { return func() { got = append(got, strings.TrimRight(v.markup(y), " ")) } }
	cursor := func() { got = append(got, fmt.Sprint(v.y, v.x, v.modes["?25"])) }
	hits := func() {
		n := 0
		for _, r := range v.cells {
			for _, c := range r {
				if c.sgr == "93;40" {
					n++
				}
			}
		}
		got = append(got, fmt.Sprint(n, " cells hit"))
	}
	a, v := helpApp(t, 80, 25,
		"3", "S", row(24), cursor, "damage", row(24), cursor, "\r", title, row(6), row(11), hits,
		"S", row(24), "\r", title, "S\r", title, row(6),
		// Not found: the message stays until the next key, which acts too.
		"S", term.KeyEsc, "xyzzy\r", title, row(24), hits, term.KeyDown, row(24),
		"S", term.KeyEsc, "damagex", term.KeyBack, row(24), term.KeyEsc, "\r", row(24), hits,
		// Around to the next documents, and back to the same one.
		"8S", term.KeyEsc, "kivij\r", title,
		"3S", term.KeyEsc, "directory(tree)\r", title, row(6),
		"S", term.KeyEsc, strings.Repeat("x", 85), row(24), cursor, term.KeyEsc, "\r",
		term.KeyEsc, term.KeyEsc)
	runHelp(t, a, v)
	want := []string{
		"{93}Search all documentation for :", "24 31 true",
		"{93}Search all documentation for : DAMAGE", "24 37 true",
		// As captured from UC2 revision 2 (menu-r2-m24s.txt).
		"(BASIC) 3.B COMPRESSING A COMPLETE DIRECTORY (TREE) INTO AN ARCHIVE 78%",
		"{97;44}3.C MAKING AN ARCHIVE {93;40}DAMAGE{97;44} PROTECTED",
		"{96;44}   The archive will become {93;40}damage{96;44} protected and it will remain {93;40}damage{96;44}",
		"30 cells hit",
		"{93}Search all documentation for : DAMAGE",
		"(BASIC) 3.D VERBOSE LIST OF THE ARCHIVE 100%",
		"(MAIN) 4.A COMMAND/OPTION SUMMARY 7%",
		"{96;44}   P         make archive {93;40}DAMAGE{96;44} PROTECTED (approx 1% overhead). Once an",
		"(MAIN) 4.A COMMAND/OPTION SUMMARY 7%",
		`{91}Could not find "XYZZY"`,
		"0 cells hit",
		"",
		"{93}Search all documentation for : DAMAGE",
		"",
		"0 cells hit",
		"(README) 1.H CREDITS 79%",
		"(BASIC) 3. UC2 BASIC COMMANDS 32%",
		"{96;44}        - B. Compressing a complete {93;40}directory(tree){96;44} into an archive",
		"{93}Search all documentation for : " + strings.Repeat("X", 48), "24 79 true",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestViewerKeys(t *testing.T) {
	var v *vt
	var got []string
	top := func() { got = append(got, v.line(1)) }
	title := titles(&v, &got)
	row := func(y int) func() { return func() { got = append(got, strings.TrimRight(v.line(y), " ")) } }
	a, v := helpApp(t, 120, 25,
		// Scroll keys drop the keys that arrived with them.
		"8", []term.Key{term.KeyDown, term.KeyDown, term.KeyDown}, top, term.KeyDown, term.KeyDown, []term.Key{term.KeyUp, term.KeyUp}, top,
		term.KeyPgDn, term.KeyPgDn, []term.Key{term.KeyPgUp, term.KeyPgUp}, top,
		// An empty search stays; Ctrl-C clears the search text.
		term.KeyHome, title, "S", term.KeyEnter, title, "S", "abc", term.KeyCtrlC, row(24), term.KeyEnter, title,
		// The search text has at most 80 characters.
		"S", strings.Repeat("x", 85), term.KeyEnter, row(24),
		// Ctrl-C returns to the menu; the end of the input leaves the viewer.
		term.KeyCtrlC, func() { got = append(got, fmt.Sprint(highlight(v))) }, "3")
	runHelp(t, a, v)
	l := manual()[8].lines
	want := []string{l[1], l[2], l[24],
		"(EXTEND) 8. UC2 EXTENDED COMMANDS 2%", "(EXTEND) 8. UC2 EXTENDED COMMANDS 2%", "Search all documentation for :", "(EXTEND) 8. UC2 EXTENDED COMMANDS 2%",
		`Could not find "` + strings.Repeat("X", 80) + `"`, "3"}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if v.line(21) != "Everything went OK" {
		t.Errorf("final screen:\n%s", strings.Join(v.lines(), "\n"))
	}
}

func TestViewerTiny(t *testing.T) {
	// Keys still act in a window too small for the help, even of 2 rows.
	var v *vt
	var got []string
	resize := func(w, h int) func() { return func() { v.resize(w, h) } }
	a, v := helpApp(t, 80, 25, "3", resize(80, 3), term.KeyResize, term.KeyEnd, term.KeyPgUp, term.KeyDown,
		resize(80, 2), term.KeyResize, term.KeyDown, term.KeyEnd, "z", term.KeyPgDn,
		resize(80, 25), term.KeyResize, titles(&v, &got), term.KeyEsc, term.KeyEsc)
	runHelp(t, a, v)
	if want := "(BASIC) 3.D VERBOSE LIST OF THE ARCHIVE 100%"; len(got) != 1 || got[0] != want {
		t.Errorf("title %q, want %q", got, want)
	}
}

func TestViewerSummary(t *testing.T) {
	// Tab prints the Z paragraph with its highlights, and ends the program.
	a, v := helpApp(t, 80, 25, "4S", "damage\r", term.KeyTab)
	runHelp(t, a, v)
	lines := manual()[4].lines
	z := lines[anchor(lines, 'Z'):]
	got := v.markups()
	if len(z) != 23 || got[23] != "{32}Everything went OK" || v.y != 24 || v.x != 0 {
		t.Errorf("summary of %d lines, cursor %d,%d:\n%s", len(z), v.y, v.x, strings.Join(got, "\n"))
	}
	for i, l := range z {
		if v.line(i) != l || !strings.HasPrefix(got[i], "{97;44}") && !strings.HasPrefix(got[i], "{96;44}") {
			t.Errorf("row %d: %q", i, got[i])
		}
	}
	if !strings.Contains(got[7], "P U   {93;40}damage{96;44} protect") {
		t.Errorf("no highlight: %q", got[7])
	}

	// A chapter without a summary leaves an empty screen.
	a, v = helpApp(t, 80, 25, "0", term.KeyTab)
	runHelp(t, a, v)
	if got := v.markups(); got[0] != "" || got[1] != "{32}Everything went OK" || strings.Join(got[2:], "") != "" {
		t.Errorf("without summary:\n%s", strings.Join(got, "\n"))
	}
}

func TestViewerResize(t *testing.T) {
	var v *vt
	var got, wide []string
	title := titles(&v, &got)
	screen := func() { got = append(got, strings.Join(v.lines(), "|")) }
	resize := func(w, h int) func() { return func() { v.resize(w, h) } }
	a, v := helpApp(t, 80, 25, "0", title,
		resize(100, 30), term.KeyResize, title, func() {
			wide = []string{v.attr(0, 0, 100), v.attr(1, 0, 100), v.attr(3, 0, 100), v.line(27), v.line(28)}
		},
		// A larger page moves back to the end of the text.
		resize(80, 25), term.KeyResize, term.KeyEnd, title,
		resize(100, 30), term.KeyResize, title, func() { got = append(got, v.line(27)) },
		// Too small: a message, until the window grows again.
		resize(60, 15), term.KeyResize, screen, "S", screen, term.KeyEsc, screen, "3", screen,
		resize(80, 25), term.KeyResize, func() { got = append(got, fmt.Sprint(highlight(v))) }, term.KeyEsc)
	runHelp(t, a, v)
	small := "The help needs 80x20 characters" + strings.Repeat("|", 14)
	want := []string{
		"(WHATSNEW) 0. WHAT IS NEW (overview of changes from version 2.3 to 2.4) 18%",
		"(WHATSNEW) 0. WHAT IS NEW (overview of changes from version 2.3 to 2.4) 22%",
		"(WHATSNEW) 0.C KNOWN PROBLEMS/ISSUES 100%",
		"(WHATSNEW) 0.C KNOWN PROBLEMS/ISSUES 100%",
		"   UC is executed if this is unwanted.",
		small, small, small, small, "3",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Wider terminals keep the layout and paint the full width.
	if w := []string{"30;47", "97;44", "96;44", manual()[0].lines[26], " Tab summary & exit  Esc menu  0-8 A-Z jump  ↑ ↓ PgUp PgDn  Search"}; !slices.Equal(wide, w) {
		t.Errorf("wide %q, want %q", wide, w)
	}
}
