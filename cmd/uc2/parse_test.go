package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/klauspost/uc2"
)

func TestParseArgs(t *testing.T) {
	tests := []struct {
		args string
		want cmd
	}{
		{"ASTF arch x", cmd{op: 'A', recurse: true, level: uc2.Fast, archives: []string{"arch"}, specs: []string{"x"}}},
		{"esf -TT #out arch *.c !*.bak", cmd{op: 'E', recurse: true, force: true, level: uc2.Tight, dest: "out", archives: []string{"arch"}, specs: []string{"*.c"}, excludes: []string{"*.bak"}}},
		{"a --recurse arch -x *.o --dest=sub --rev=all --level super", cmd{op: 'A', recurse: true, level: uc2.SuperTight, dest: "sub", archives: []string{"arch"}, excludes: []string{"*.o"}, rev: revAll}},
		{"list arch file;2 -d out", cmd{op: 'L', dest: "out", archives: []string{"arch"}, specs: []string{"file;2"}}},
		{"A !newer -I arch", cmd{op: 'A', newer: true, incremental: true, archives: []string{"arch"}}},
		{"aib arch", cmd{op: 'A', archives: []string{"arch"}}},
		{"atstp arch", cmd{op: 'A', level: uc2.SuperTight, protect: true, archives: []string{"arch"}}},
		{"am arch", cmd{op: 'A', move: true, archives: []string{"arch"}}},
		{"f arch", cmd{op: 'A', freshen: true, archives: []string{"arch"}}},
		{"-x arch", cmd{op: 'E', archives: []string{"arch"}}},
		{"e ##dst arch", cmd{op: 'E', dest: "dst", destSrc: true, archives: []string{"arch"}}},
		{"E#dst arch", cmd{op: 'E', dest: "dst", archives: []string{"arch"}}},
		{"e -- arch -x !y", cmd{op: 'E', archives: []string{"arch"}, specs: []string{"-x", "!y"}}},
		{"t a b c", cmd{op: 'T', archives: []string{"a", "b", "c"}}},
		{"--threads=3 v arch --charset=850", cmd{op: 'V', threads: 3, charset: uc2.CP850, archives: []string{"arch"}}},
	}
	for _, tc := range tests {
		t.Run(tc.args, func(t *testing.T) {
			cmds, _, err := parseArgs(strings.Fields(tc.args))
			if err != nil {
				t.Fatal(err)
			}
			if len(cmds) != 1 {
				t.Fatalf("got %d commands", len(cmds))
			}
			got, want := *cmds[0], tc.want
			if want.rev == 0 {
				want.rev = revDefault
			}
			want.name = commandNames[want.op]
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}

func TestParseSegmentsAndGlobals(t *testing.T) {
	cmds, g, err := parseArgs([]string{"l", "a", "&", "v", "b", "-q", "&"})
	if err != nil || len(cmds) != 2 || cmds[0].op != 'L' || cmds[1].op != 'V' || cmds[1].archives[0] != "b" || g.verbosity != quiet {
		t.Fatalf("got %+v %+v %v", cmds, g, err)
	}
	if runtime.GOOS == "windows" {
		cmds, _, err = parseArgs([]string{"e", "/S", "arch"})
		if err != nil || !cmds[0].recurse {
			t.Fatalf("/S: %+v %v", cmds, err)
		}
	}
}

func TestParseScript(t *testing.T) {
	t.Chdir(t.TempDir())
	os.WriteFile("list.USC", []byte("L arch\r\n*.txt & V\tarch2\x1a"), 0o666)
	cmds, _, err := parseArgs([]string{"@list"})
	if err != nil || len(cmds) != 2 || cmds[0].specs[0] != "*.txt" || cmds[1].archives[0] != "arch2" {
		t.Fatalf("got %+v %v", cmds, err)
	}
	os.WriteFile("loop", []byte("@loop"), 0o666)
	if _, _, err := parseArgs([]string{"@loop"}); code(err) != sevCmdLine {
		t.Fatalf("cyclic script: %v", err)
	}
	if _, _, err := parseArgs([]string{"L", "@missing"}); code(err) != sevCmdLine {
		t.Fatalf("missing script: %v", err)
	}
}

func code(err error) int {
	var fe *fatalError
	if errors.As(err, &fe) {
		return fe.code
	}
	return 0
}

func TestParseErrors(t *testing.T) {
	for _, args := range []string{
		"Q arch", "AZ arch", "AT arch", "ATX arch", "C arch", "$RED arch", "D arch", "D arch !x", "LM arch",
		"A", "A -S", "A !DTT=19930101 arch", "A !BOGUS arch", "a --bogus arch", "a arch --level=max",
		"a arch --rev", "a arch --recurse=1", "APU arch", "a arch --threads=0",
	} {
		if _, _, err := parseArgs(strings.Fields(args)); code(err) != sevCmdLine {
			t.Errorf("%q: got %v, want command line error", args, err)
		}
	}
}

func TestArchiveNames(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	os.WriteFile("x1.UC2", nil, 0o666)
	os.WriteFile("x2.UC2", nil, 0o666)
	a := newTestApp("")
	got := a.archives(&cmd{archives: []string{"arch", "arch.", "a.b", filepath.Join("d.x", "arch"), "x*"}})
	want := []string{"arch.UC2", "arch", "a.b", filepath.Join("d.x", "arch.UC2"), "x1.UC2", "x2.UC2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}

	// '[' is no wildcard, as in UC2.
	os.WriteFile("backup2.UC2", nil, 0o666)
	os.WriteFile("y[1].UC2", nil, 0o666)
	os.WriteFile("y1.UC2", nil, 0o666)
	got = a.archives(&cmd{archives: []string{"backup[2024]", "y*[1]"}})
	want = []string{"backup[2024].UC2", "y[1].UC2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMasks(t *testing.T) {
	tests := []struct {
		mask, long, short string
		want              bool
	}{
		{"*.*", "Makefile", "MAKEFILE", true},
		{"*.*", "no dot long name", "", true},
		{"*.txt", "A.TXT", "A.TXT", true},
		{"*.tx", "a.txt", "A.TXT", false},
		{"FILE?.TXT", "file.txt", "FILE.TXT", true},
		{"FILE?.TXT", "file12.txt", "FILE12.TXT", false},
		{"read*", "readme-long.md", "README~1.MD", true},
		{"README~1.MD", "readme-long.md", "README~1.MD", true},
		{"*.text", "A long file name.text", "ALONGF~1.TEX", true},
		{"a[1].txt", "a[1].txt", "", true},
		{"a[12].txt", "a1.txt", "", false},
		{"*[1]*", "x[1].c", "", true},
		{"*[1]*", "x1.c", "", false},
		{"*.c", "x.h", "X.H", false},
	}
	for _, tc := range tests {
		if got := compileMask(tc.mask).matchName(tc.long, tc.short); got != tc.want {
			t.Errorf("%q vs %q/%q: got %v", tc.mask, tc.long, tc.short, got)
		}
	}
	// DOS semantics of '*' alone: extensionless names only.
	p, _ := fcbPattern("*")
	if n, _ := fcbName("A.TXT"); fcbMatch(p, n) {
		t.Error("* matched A.TXT")
	}
	if n, _ := fcbName("README"); !fcbMatch(p, n) {
		t.Error("* did not match README")
	}

	m := compileMask(`\sub\dir\*.c;3`)
	if !reflect.DeepEqual(m.dirs, []string{"sub", "dir"}) || m.name != "*.c" || m.rev != 3 {
		t.Errorf("got %+v", m)
	}
	for s, rev := range map[string]int{"x;*": revAll, "x;0": 0, "x": revDefault} {
		if m := compileMask(s); m.rev != rev || m.name != "x" {
			t.Errorf("%q: got %+v", s, m)
		}
	}
	if m := compileMask("x;abc"); m.name != "x;abc" {
		t.Errorf("literal ';': got %+v", m)
	}
	if m := compileMask("sub/"); m.name != "*.*" || m.dirs[0] != "sub" {
		t.Errorf("trailing separator: got %+v", m)
	}

	path := func(s string) []string { return strings.Split(s, "/") }
	m = compileMask(`sub\*.c`)
	switch {
	case !m.matchPath(path("SUB/x.c"), nil, false):
		t.Error("case-insensitive directory")
	case m.matchPath(path("sub/deep/x.c"), nil, false):
		t.Error("matched below the mask directory without recursion")
	case !m.matchPath(path("sub/deep/x.c"), nil, true):
		t.Error("no match below the mask directory with recursion")
	case !m.matchPath(path("sublong/x.c"), path("SUB/X.C"), false):
		t.Error("8.3 directory name")
	case !compileMask("tools").matchPath(path("tools/x/y.z"), nil, true):
		t.Error("matching directory does not select its contents")
	}
}

func TestMapPath(t *testing.T) {
	for in, want := range map[string]string{
		"CON":           "_CON",
		"con.txt":       "_con.txt",
		"dir/LPT1.x/ok": "dir/_LPT1.x/ok",
		"clock$":        "_clock$",
		"COM10":         "COM10",
		"a:b*c?.txt":    "a_b_c_.txt",
		"name. ":        "name__",
		"sub/":          "sub/",
		"nul/":          "_nul/",
	} {
		if got := mapPath(in); got != want {
			t.Errorf("mapPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatting(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", 4294967295: "4,294,967,295"} {
		if got := neat(n); got != want {
			t.Errorf("neat(%d) = %q", n, got)
		}
	}
	if got, _ := ratio(1000, 2340); got != "1:2.34  (57.3% reduction, 42.7% left)" {
		t.Errorf("ratio: %q", got)
	}
	if _, ok := ratio(1000, 1000); ok {
		t.Error("ratio 1:1 shown")
	}
}
