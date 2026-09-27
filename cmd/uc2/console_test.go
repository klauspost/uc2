package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/uc2"
)

// screen renders terminal output: backspace and carriage return move the
// cursor, text overwrites.
func screen(s string) []string {
	lines := [][]rune{{}}
	col := 0
	for _, r := range s {
		cur := &lines[len(lines)-1]
		switch r {
		case '\n':
			lines = append(lines, []rune{})
			col = 0
		case '\r':
			col = 0
		case '\b':
			col--
		default:
			for len(*cur) <= col {
				*cur = append(*cur, ' ')
			}
			(*cur)[col] = r
			col++
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = string(l)
	}
	return out
}

func TestPainter(t *testing.T) {
	var b bytes.Buffer
	p := &painter{w: &b, color: true}
	p.Write([]byte(cOK + "OK" + cN + " x\n"))
	p.Write([]byte(cErr + "err"))
	p.Write([]byte(" more\n" + cN))
	want := "\x1b[32mOK\x1b[0m x\n\x1b[91merr\x1b[0m\x1b[91m more\n\x1b[0m"
	if b.String() != want {
		t.Errorf("colored %q, want %q", b.String(), want)
	}
	b.Reset()
	p = &painter{w: &b}
	p.Write([]byte(cH + "--> Directory of \\\n" + cN + "■■ \x01\x02\x09x"))
	if b.String() != "--> Directory of \\\n■■ x" || p.col != 4 {
		t.Errorf("plain %q, column %d", b.String(), p.col)
	}
	// Arguments cannot inject color codes or escapes.
	a := newTestApp("")
	a.out.color = true
	a.printf(cOK+"%s\n", "\x08\x1b[2Jevil")
	a.out.Flush()
	if s := a.stdout.(*bytes.Buffer).String(); s != "\x1b[32m??[2Jevil\n\x1b[0m" {
		t.Errorf("argument output %q", s)
	}
}

func TestColorOn(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	for _, tc := range []struct {
		mode, noColor, term string
		terminal, want      bool
	}{
		{"", "", "xterm", true, true},
		{"auto", "", "xterm", false, false},
		{"", "1", "xterm", true, false},
		{"", "", "dumb", true, false},
		{"always", "1", "dumb", false, true},
		{"never", "", "xterm", true, false},
	} {
		t.Setenv("NO_COLOR", tc.noColor)
		t.Setenv("TERM", tc.term)
		if got := colorOn(tc.mode, tc.terminal); got != tc.want {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
}

func TestLogo(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a"})
	rule := strings.Repeat("═", 75)
	logo := rule + "\n ░███   ░███  ░████      UltraCompressor II - Go - devel\n░█  ░█   ░█   ░█  ░█\n░█████   ░█   ░████\n░█  ░█   ░█   ░█\n░█  ░█  ░███  ░█\n" + rule + "\n\n"
	// Also redirected, as in UC2.
	if out := uc2Run(t, 0, "a", "arch", "a.txt"); !strings.HasPrefix(out, logo+"Adding files to arch.UC2\n") {
		t.Errorf("no logo:\n%s", out)
	}
	if out := uc2Run(t, 0, "l", "-q", "arch"); !strings.HasPrefix(out, "arch.UC2\n") {
		t.Errorf("logo at quiet level:\n%s", out)
	}
	t.Setenv("UC2_NO_HIGH_ASCII", "")
	if out := uc2Run(t, 0, "l", "arch"); !strings.HasPrefix(out, strings.Repeat("=", 75)+"\n @@@@   @@@@  @@@@@      Ultra") {
		t.Errorf("UC2_NO_HIGH_ASCII ignored:\n%s", out)
	}
	for _, args := range [][]string{{"--help"}, {"l", "x", "--help", "--color=always"}, {"--version"}} {
		if out := uc2Run(t, 0, args...); strings.Contains(out, "@@@@") || strings.ContainsAny(out, "═\x1b") {
			t.Errorf("%q has a logo or colors:\n%s", args, out)
		}
	}
	if out := uc2Run(t, 0, "l", "--color=always", "arch"); !strings.HasPrefix(out, "\x1b[36m=") || !strings.Contains(out, "\x1b[96mUltraCompressor II - Go - devel") {
		t.Errorf("logo not cyan:\n%q", out)
	}
	for _, v := range []string{"1.2.3", "v1.2.3"} {
		buildVersion = v
		if got := version(); got != "v1.2.3" {
			t.Errorf("version with %q = %q", v, got)
		}
	}
	buildVersion = ""
	uc2Run(t, sevCmdLine, "l", "--color=blue", "arch")
}

func TestStreams(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a"})
	var out, errs bytes.Buffer
	if code := run([]string{"a", "arch", "a.txt", "none.xyz"}, strings.NewReader(""), &out, &errs); code != sevNoMatch {
		t.Fatalf("exit %d", code)
	}
	o, e := out.String(), errs.String()
	if !strings.Contains(o, "Compressing a.txt DONE\n") || strings.Contains(o, "WARNING") || strings.Contains(o, "reported") || strings.Contains(o, "\x1b") {
		t.Errorf("stdout:\n%s", o)
	}
	if e != " WARNING 20: no file found matching none.xyz\n\n1 warning has been reported \n" {
		t.Errorf("stderr %q", e)
	}
	// The warning comes after the archive is written, as in UC2.
	out.Reset()
	errs.Reset()
	run([]string{"a", "--color=always", "arch2", "a.txt", "none.xyz"}, strings.NewReader(""), &out, &errs)
	if e := errs.String(); e != "\x1b[91m WARNING 20: no file found matching none.xyz\n\x1b[0m\n\x1b[91m1 warning has been reported \n\x1b[0m" {
		t.Errorf("colored stderr %q", e)
	}
	contains(t, out.String(), "\x1b[32mAdding files to arch2.UC2\n\x1b[0m", "Compressing a.txt \x1b[32mDONE\x1b[0m\n")
	out.Reset()
	run([]string{"l", "--color=always", "arch"}, strings.NewReader(""), &out, &errs)
	contains(t, out.String(), "\n\x1b[32mEverything went OK\n\x1b[0m")
}

// barApp returns an app writing to a terminal, with a clock the test sets.
func barApp(clock *time.Time) (*app, *bytes.Buffer) {
	a := newTestApp("")
	a.termOut = true
	a.now = func() time.Time { return *clock }
	return a, a.stdout.(*bytes.Buffer)
}

func TestProgressBar(t *testing.T) {
	clock := time.Unix(0, 0)
	a, out := barApp(&clock)
	a.printf(cN+"Compressing %s ", "FOO.TXT")
	a.startBar(lvStd, 1000)
	var states []string
	step := func(n int64, d time.Duration) {
		clock = clock.Add(d)
		a.hint(n)
		a.out.Flush()
		states = append(states, screen(out.String())[0])
	}
	step(0, 0)
	step(400, 0) // 2 blocks
	step(10, 0)  // same block count, same tick: no redraw
	step(0, tick)
	step(800, 0)
	a.endBar()
	a.printf(cOK + "DONE\n")
	a.out.Flush()
	want := []string{
		"Compressing FOO.TXT ■····· |",
		"Compressing FOO.TXT ■■···· /",
		"Compressing FOO.TXT ■■···· /",
		"Compressing FOO.TXT ■■···· -",
		"Compressing FOO.TXT ■■■■■■ \\",
	}
	if strings.Join(states, "\n") != strings.Join(want, "\n") {
		t.Errorf("states:\n%s\nwant:\n%s", strings.Join(states, "\n"), strings.Join(want, "\n"))
	}
	if s := screen(out.String()); s[0] != "Compressing FOO.TXT ■■■■■■ DONE" {
		t.Errorf("final %q", s[0])
	}

	// Unknown size: the bar grows with the clock. Errors go to stderr after
	// the bar is finished and the line ended.
	out.Reset()
	a.printf(cN + "Verifying X ")
	a.startBar(lvStd, -1)
	for range 31 {
		step(0, tick)
	}
	if s := screen(out.String())[0]; !strings.HasPrefix(s, "Verifying X ■■■··· ") {
		t.Errorf("after 31 ticks %q", s)
	}
	a.errorf(sevDamaged, "file %s is damaged", "X")
	if s := screen(out.String()); len(s) != 2 || s[0] != "Verifying X ■■■■■■  " || s[1] != "" {
		t.Errorf("before the error %q", s)
	}
	if e := a.stderr.(*bytes.Buffer).String(); e != " ERROR 90: file X is damaged\n" {
		t.Errorf("stderr %q", e)
	}

	// A bar never starts beyond column 69 of an 80 column line.
	out.Reset()
	a.printf(cN+"Decompressing %s ", strings.Repeat("X", 60))
	a.startBar(lvStd, 0)
	a.endBar()
	a.printf(cOK + "OK\n")
	a.out.Flush()
	if s := screen(out.String()); len(s) != 3 || s[1] != strings.Repeat(" ", 68)+"■■■■■■ OK" {
		t.Errorf("wrapped %q", s)
	}

	// Bars only on a terminal, never at quiet level.
	for _, b := range []struct{ term, quiet bool }{{false, false}, {true, true}} {
		a, out := barApp(&clock)
		a.termOut = b.term
		if b.quiet {
			a.verbosity = quiet
		}
		a.startBar(lvStd, 10)
		a.hint(10)
		a.endBar()
		a.out.Flush()
		if out.Len() != 0 {
			t.Errorf("%+v: bar %q", b, out)
		}
	}
}

func TestPhaseAnimation(t *testing.T) {
	a := newTestApp("")
	a.termOut = true
	out := a.stdout.(*bytes.Buffer)
	a.phase(lvNormal, "Optimizing compression", func() error {
		time.Sleep(10 * tick)
		return nil
	})
	a.out.Flush()
	if s := out.String(); !strings.ContainsAny(s, `|/-\`) || strings.TrimRight(screen(s)[0], " ") != "Optimizing compression ■■■■■■" {
		t.Errorf("phase %q", s)
	}
	a.verbosity = verbose // normal only
	out.Reset()
	if err := a.phase(lvNormal, "Analyzing", func() error { return errors.New("x") }); err == nil || out.Len() != 0 {
		t.Errorf("verbose phase %q, %v", out, err)
	}
}

// keys returns a key reader for the given presses.
func keys(k ...string) func() ([]byte, error) {
	return func() ([]byte, error) {
		if len(k) == 0 {
			return nil, errors.New("end of input")
		}
		b := []byte(k[0])
		k = k[1:]
		return b, nil
	}
}

func TestPromptKeys(t *testing.T) {
	opts := []option{{"", "Y", "es"}, {"", "N", "o"}, {"", "A", "lways overwrite files"}, {"N", "e", "ver overwrite files"}}
	for _, tc := range []struct {
		keys   []string
		choice int
		code   int
		echo   string
	}{
		{[]string{"x", "\r", "\x1b", "\x1b[A", "e"}, 3, 0, "X [Enter] [Esc] ■ E"},
		{[]string{"2"}, 1, 0, "2"},
		{[]string{"y"}, 0, 0, "Y"},
		{[]string{"+"}, 0, sevAbort, "+"},
		{[]string{"\x03"}, 0, sevAbort, "■"},
		{nil, -1, 0, ""},
	} {
		a := newTestApp("")
		a.key = keys(tc.keys...)
		var slept time.Duration
		a.sleep = func(d time.Duration) { slept += d }
		choice, err := a.ask("Overwrite file A.TXT ?", opts)
		if choice != tc.choice || code(err) != tc.code {
			t.Errorf("%q: choice %d, %v", tc.keys, choice, err)
		}
		s := a.stderr.(*bytes.Buffer).String()
		head := "\nOverwrite file A.TXT ?\n   1 -> Yes\n   2 -> No\n   3 -> Always overwrite files\n   4 -> Never overwrite files\n"
		prompt := "CHOICE (+=Abort) ? "
		if !strings.HasPrefix(s, head+prompt) {
			t.Errorf("%q: menu %q", tc.keys, s)
		}
		// Echoed keys, without the erased wrong choices.
		var shown []string
		for _, l := range strings.Split(strings.TrimPrefix(s, head), prompt)[1:] {
			shown = append(shown, strings.TrimSuffix(strings.Split(l, " *** WRONG CHOICE ***")[0], "\n"))
		}
		if got := strings.Join(shown, " "); got != tc.echo {
			t.Errorf("%q: echoed %q, want %q", tc.keys, got, tc.echo)
		}
		if wrong := strings.Count(s, " *** WRONG CHOICE ***\r"+strings.Repeat(" ", 57)+"\r"); slept != time.Duration(wrong)*500*time.Millisecond {
			t.Errorf("%q: %d wrong choices, slept %v", tc.keys, wrong, slept)
		}
	}

	// Colored layout, with the hot keys highlighted.
	a := newTestApp("")
	a.errOut.color = true
	a.key = keys("n")
	a.ask("Overwrite file A.TXT ?", opts)
	contains(t, a.stderr.(*bytes.Buffer).String(), "\n\x1b[93mOverwrite file A.TXT ?\n\x1b[0m",
		"\x1b[93m   4 \x1b[0m-> N\x1b[93me\x1b[0mver overwrite files\n",
		"\x1b[93mCHOICE \x1b[0m(\x1b[93m+\x1b[0m=Abort) \x1b[93m? \x1b[0m\x1b[93mN\x1b[0m\n")
}

func TestPromptKeysExtract(t *testing.T) {
	setupArchive(t, "")
	writeFiles(t, map[string]string{"a.txt": "local", "README": "local"}, stamp)
	a := newTestApp("")
	a.key = keys("q", "a")
	a.sleep = func(time.Duration) {}
	if code := a.run([]string{"e", "arch", "a.txt", "README"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	checkFile(t, "a.txt", "alpha\n")
	checkFile(t, "README", "read me\n")
	if s := a.stderr.(*bytes.Buffer).String(); strings.Count(s, "Overwrite file") != 1 || !strings.Contains(s, "? Q *** WRONG CHOICE ***") {
		t.Errorf("prompt:\n%s", s)
	}
}

func TestUpdatedArchiveLine(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a", "sub/b.txt": "bb", "c.txt": "ccc", "note.txt": "comment"})
	check := func(out string) {
		t.Helper()
		r, err := uc2.OpenReader("arch.UC2")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		tl := &tally{}
		for _, f := range r.File {
			tl.add(f)
		}
		contains(t, out, "Updated archive "+tl.String()+"\n")
	}
	check(uc2Run(t, 0, "as", "arch", "a.txt", "sub/*.*"))
	check(uc2Run(t, 0, "ai", "arch", "c.txt"))
	check(uc2Run(t, 0, "r", "arch", "--comment-file", "note.txt"))
	check(uc2Run(t, 0, "p", "arch"))
	check(uc2Run(t, 0, "d", "arch", "c.txt"))
	os.Chtimes("a.txt", stamp.Add(time.Hour), stamp.Add(time.Hour))
	check(uc2Run(t, 0, "a", "arch", "a.txt"))
	check(uc2Run(t, 0, "em", "#out", "arch", "a.txt"))
	check(uc2Run(t, 0, "u", "arch"))
	for _, tc := range []struct {
		t    tally
		want string
	}{
		{tally{}, "is empty "},
		{tally{files: 2, size: 23028}, "contains 2 files (23,028 bytes)  "},
		{tally{files: 1, size: 1, dirs: 1}, "contains 1 file (1 byte) and 1 directory "},
		{tally{files: 7, size: 62844, dirs: 2}, "contains 7 files (62,844 bytes) and 2 directories  "},
	} {
		if s := tc.t.String(); s != tc.want {
			t.Errorf("%+v: %q, want %q", tc.t, s, tc.want)
		}
	}
}
