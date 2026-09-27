package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/uc2"
	"github.com/klauspost/uc2/internal/format"
	"github.com/klauspost/uc2/internal/ultra"
)

func newTestApp(stdin string) *app {
	return &app{in: bufio.NewReader(strings.NewReader(stdin)), rawIn: strings.NewReader(stdin),
		out: bufio.NewWriter(new(bytes.Buffer)), stderr: new(bytes.Buffer), verbosity: normal}
}

// uc2Run runs the command line in process, checks the exit code and
// returns stdout followed by stderr.
func uc2Run(t *testing.T, want int, args ...string) string {
	t.Helper()
	return uc2Input(t, want, "", args...)
}

func uc2Input(t *testing.T, want int, stdin string, args ...string) string {
	t.Helper()
	var out, errs bytes.Buffer
	if got := run(args, strings.NewReader(stdin), &out, &errs); got != want {
		t.Fatalf("uc2 %q: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", args, got, want, out.String(), errs.String())
	}
	return out.String() + errs.String()
}

func contains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, s := range want {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

var stamp = time.Date(2020, 5, 17, 13, 30, 58, 0, time.Local)

// setup creates files (name -> content, directories end in "/") in a new
// current directory.
func setup(t *testing.T, files map[string]string) {
	t.Chdir(t.TempDir())
	writeFiles(t, files, stamp)
}

func writeFiles(t *testing.T, files map[string]string, mod time.Time) {
	t.Helper()
	for name, data := range files {
		p := filepath.FromSlash(name)
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(p, 0o777); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o666); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
}

// contents returns the files of an archive as "NAME[;rev]" -> data.
func contents(t *testing.T, arch string) map[string]string {
	t.Helper()
	if filepath.Ext(arch) == "" {
		arch += ".UC2"
	}
	r, err := uc2.OpenReader(arch)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	m := map[string]string{}
	for _, f := range r.File {
		if isDir(f) {
			m[f.Name] = ""
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if _, err := b.ReadFrom(rc); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		m[strings.ReplaceAll(dispFile(f), `\`, "/")] = b.String()
	}
	return m
}

func checkContents(t *testing.T, arch string, want map[string]string) {
	t.Helper()
	got := contents(t, arch)
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s: %s = %q (present %v), want %q", arch, k, g, ok, v)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok && !strings.HasSuffix(k, "/") {
			t.Errorf("%s: unexpected %s", arch, k)
		}
	}
}

func checkFile(t *testing.T, name, want string) {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash(name))
	if err != nil {
		t.Error(err)
		return
	}
	if string(b) != want {
		t.Errorf("%s: got %q, want %q", name, b, want)
	}
}

func notExist(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Lstat(filepath.FromSlash(name)); err == nil {
		t.Errorf("%s exists", name)
	}
}

var testTree = map[string]string{
	"src/a.txt":                   "alpha\n",
	"src/README":                  "read me\n",
	"src/b.bak":                   "backup\n",
	"src/sub/c.txt":               strings.Repeat("charlie ", 1000),
	"src/sub/Long file name.text": "long\n",
	"src/sub/deep/d.txt":          "delta\n",
	"src/empty/":                  "",
}

// setupArchive creates tree and arch.UC2 holding the contents of src.
func setupArchive(t *testing.T, opts string) {
	setup(t, testTree)
	os.Chdir("src")
	uc2Run(t, 0, "AS"+opts, "../arch", "*.*")
	os.Chdir("..")
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"-?"}, {"--help"}, {"l", "x", "--help"}} {
		contains(t, uc2Run(t, 0, args...), "SYNTAX: uc2 command")
	}
}

func TestRoundTrip(t *testing.T) {
	setup(t, testTree)
	os.Chdir("src")
	contains(t, uc2Run(t, 0, "AS", "../arch", "*.*"), "Adding files to ../arch.UC2", `Compressing sub\deep\d.txt DONE`,
		"Updated archive contains 6 files (8,032 bytes) and 3 directories", "Everything went OK")
	os.Chdir("..")

	contains(t, uc2Run(t, 0, "L", "arch"), "--> Directory of \\\n[EMPTY]         [SUB]           ", "A.TXT           ",
		"files listed = 3 (21 bytes)\n")
	contains(t, uc2Run(t, 0, "vs", "arch"),
		"--> Directory of \\sub\\\n\nDEEP         <DIR> deep\n",
		"LONGFI~1 TEX            5  MAY-17-2020  13:30:58  Arch Long file name.text\n",
		"      2 matching files\n",
		"\nArchive is NOT damage protected, archive has no volume label\n",
		"files in archive       = 6        total length all files    = 8,032 bytes\n",
		"directories in archive = 3        archive length            = ")

	uc2Run(t, 0, "ES", "#out", "arch")
	for name, data := range testTree {
		name = "out" + strings.TrimPrefix(name, "src")
		if strings.HasSuffix(name, "/") {
			if fi, err := os.Stat(name); err != nil || !fi.IsDir() {
				t.Errorf("directory %s not created", name)
			}
			continue
		}
		checkFile(t, name, data)
		if fi, err := os.Stat(name); err == nil && !fi.ModTime().Equal(stamp) {
			t.Errorf("%s: time %v, want %v", name, fi.ModTime(), stamp)
		}
	}
}

func TestExtractSelection(t *testing.T) {
	setupArchive(t, "")

	uc2Run(t, 0, "E", "#flat", "arch")
	checkFile(t, "flat/a.txt", "alpha\n")
	notExist(t, "flat/sub")

	uc2Run(t, 0, "E", "#m", "arch", `sub\*.txt`)
	checkFile(t, "m/c.txt", testTree["src/sub/c.txt"])
	notExist(t, "m/sub")

	uc2Run(t, 0, "e", "##p", "arch", "/SUB/*.TXT")
	checkFile(t, "p/sub/c.txt", testTree["src/sub/c.txt"])

	uc2Run(t, 0, "es", "arch", "-d", "x", `!sub\deep\`, "!*.bak")
	checkFile(t, "x/sub/c.txt", testTree["src/sub/c.txt"])
	notExist(t, "x/sub/deep")
	notExist(t, "x/b.bak")

	uc2Run(t, 0, "es", "#t", "arch", "deep")
	checkFile(t, "t/sub/deep/d.txt", "delta\n")
	notExist(t, "t/a.txt")

	contains(t, uc2Run(t, sevNoMatch, "e", "#n", "arch", "nothing.xyz"), " WARNING 20: no file found matching nothing.xyz")
}

func TestExtractExisting(t *testing.T) {
	setupArchive(t, "")
	uc2Run(t, 0, "e", "arch", "a.txt")
	contains(t, uc2Run(t, 0, "e", "arch", "a.txt"), "Smart skipping a.txt")

	writeFiles(t, map[string]string{"a.txt": "local"}, stamp)
	contains(t, uc2Run(t, sevSkipped, "e", "arch", "a.txt"), " WARNING 30: skipping a.txt (already exists)")
	checkFile(t, "a.txt", "local")
	uc2Run(t, 0, "e", "!newer", "arch", "a.txt")
	checkFile(t, "a.txt", "local")
	uc2Run(t, 0, "ef", "arch", "a.txt")
	checkFile(t, "a.txt", "alpha\n")

	// Read-only attribute is restored and does not prevent overwriting.
	os.Chmod(filepath.Join("src", "README"), 0o444)
	os.Chtimes(filepath.Join("src", "README"), stamp.Add(time.Hour), stamp.Add(time.Hour))
	uc2Run(t, 0, "a", "arch", filepath.Join("src", "README"))
	uc2Run(t, 0, "e", "arch", "README")
	if fi, err := os.Stat("README"); err != nil || fi.Mode()&0o200 != 0 {
		t.Errorf("README not read-only: %v %v", fi, err)
	}
	os.Chtimes("README", time.Now(), time.Now())
	uc2Run(t, 0, "e", "-f", "arch", "README")
	checkFile(t, "README", "read me\n")
}

func TestPrompt(t *testing.T) {
	setupArchive(t, "")
	writeFiles(t, map[string]string{"a.txt": "local", "README": "local"}, stamp)
	for _, tc := range []struct {
		input string
		code  int
		want  string
	}{
		{"x\n2\n", 0, "local"},
		{"N\n", 0, "local"},
		{"+\n", sevAbort, "local"},
		{"", sevSkipped, "local"}, // end of input: not interactive after all
		{"1\n", 0, "alpha\n"},
	} {
		a := newTestApp(tc.input)
		a.tty = true
		if got := a.run([]string{"e", "arch", "a.txt"}); got != tc.code {
			t.Errorf("input %q: exit %d, want %d", tc.input, got, tc.code)
		}
		checkFile(t, "a.txt", tc.want)
		writeFiles(t, map[string]string{"a.txt": "local"}, stamp)
	}
	a := newTestApp("a\n")
	a.tty = true
	if a.run([]string{"e", "arch", "a.txt", "README"}) != 0 {
		t.Fatal("always: failed")
	}
	checkFile(t, "a.txt", "alpha\n")
	checkFile(t, "README", "read me\n")
	if s := a.stderr.(*bytes.Buffer).String(); strings.Count(s, "Overwrite file") != 1 {
		t.Errorf("always asked again:\n%s", s)
	}
}

func TestAddOptions(t *testing.T) {
	setup(t, testTree)
	uc2Run(t, 0, "as", "arch", `src\*.txt`)
	checkContents(t, "arch", map[string]string{"a.txt": "alpha\n", "sub/c.txt": testTree["src/sub/c.txt"], "sub/deep/d.txt": "delta\n"})

	uc2Run(t, 0, "a", "x", "src/", "#docs/old")
	checkContents(t, "x", map[string]string{"docs/old/a.txt": "alpha\n", "docs/old/README": "read me\n", "docs/old/b.bak": "backup\n"})

	uc2Run(t, 0, "a", "y", "##", "src/*.txt")
	checkContents(t, "y", map[string]string{"src/a.txt": "alpha\n"})

	uc2Run(t, 0, "add", "--recurse", "z", "src", "-x", "*.bak", "--exclude=src/sub/deep/")
	checkContents(t, "z", map[string]string{"src/a.txt": "alpha\n", "src/README": "read me\n", "src/sub/c.txt": testTree["src/sub/c.txt"], "src/sub/Long file name.text": "long\n"})

	contains(t, uc2Run(t, sevNoMatch, "a", "w", "none*.xyz"), "no file found matching none*.xyz")
	notExist(t, "w.UC2")
}

func TestSmartSkipAndReplace(t *testing.T) {
	setupArchive(t, "")
	before, _ := os.Stat("arch.UC2")
	os.Chdir("src")
	out := uc2Run(t, 0, "as", "../arch")
	if strings.Contains(out, "Compressing") || !strings.Contains(out, "Smart skipped 6 files (8,032 bytes)") {
		t.Errorf("unchanged files were added:\n%s", out)
	}
	if after, _ := os.Stat("../arch.UC2"); !after.ModTime().Equal(before.ModTime()) {
		t.Error("archive rewritten without changes")
	}
	writeFiles(t, map[string]string{"a.txt": "alpha 2\n"}, stamp.Add(time.Hour))
	out = uc2Run(t, 0, "as", "../arch")
	contains(t, out, "Compressing a.txt DONE")
	if strings.Count(out, "Compressing") != 1 {
		t.Errorf("unchanged files were added:\n%s", out)
	}
	os.Chdir("..")
	checkContents(t, "arch", map[string]string{"a.txt": "alpha 2\n", "README": "read me\n", "b.bak": "backup\n",
		"sub/c.txt": testTree["src/sub/c.txt"], "sub/Long file name.text": "long\n", "sub/deep/d.txt": "delta\n"})
}

func TestIncrementalRevisions(t *testing.T) {
	setup(t, map[string]string{"f.txt": "v1", "g.txt": "g"})
	uc2Run(t, 0, "a", "arch", "f.txt")
	writeFiles(t, map[string]string{"f.txt": "v2"}, stamp.Add(time.Hour))
	uc2Run(t, 0, "ai", "arch", "f.txt", "g.txt")
	checkContents(t, "arch", map[string]string{"f.txt;1": "v1", "f.txt": "v2", "g.txt": "g"})
	contains(t, uc2Run(t, 0, "v", "arch"), "F        TXT;1          2  MAY-17-2020  13:30:58  Arch f.txt\n"+
		"F        TXT            2  MAY-17-2020  14:30:58", "      2 matching files (3 file revisions)\n")
	if out := uc2Run(t, 0, "l", "arch"); strings.Contains(out, "F.TXT;1") {
		t.Errorf("L lists old revisions by default:\n%s", out)
	}
	contains(t, uc2Run(t, 0, "l", "arch", "--rev=all"), "F.TXT;1")

	uc2Run(t, 0, "e", "#old", "arch", "f.txt;1")
	checkFile(t, "old/f.txt", "v1")

	// Basic mode replaces the newest revision and keeps the older ones.
	writeFiles(t, map[string]string{"f.txt": "v3"}, stamp.Add(2*time.Hour))
	uc2Run(t, 0, "a", "arch", "f.txt")
	checkContents(t, "arch", map[string]string{"f.txt;1": "v1", "f.txt": "v3", "g.txt": "g"})

	// Delete takes the newest revision unless told otherwise.
	contains(t, uc2Run(t, 0, "d", "arch", "f.txt"), "Deleting f.txt\n")
	checkContents(t, "arch", map[string]string{"f.txt": "v1", "g.txt": "g"})
	uc2Run(t, 0, "d", "arch", "*.*;*")
	checkContents(t, "arch", map[string]string{})
}

func TestFreshenMove(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	uc2Run(t, 0, "a", "arch", "a.txt")
	writeFiles(t, map[string]string{"a.txt": "a2"}, stamp.Add(time.Hour))
	uc2Run(t, 0, "f", "arch")
	checkContents(t, "arch", map[string]string{"a.txt": "a2"})

	contains(t, uc2Run(t, 0, "m", "arch", "b.txt"), "Moving files")
	notExist(t, "b.txt")
	checkFile(t, "a.txt", "a2")
	checkContents(t, "arch", map[string]string{"a.txt": "a2", "b.txt": "b"})

	// Move extraction removes the extracted revisions from the archive.
	uc2Run(t, 0, "em", "#out", "arch", "b.txt")
	checkFile(t, "out/b.txt", "b")
	checkContents(t, "arch", map[string]string{"a.txt": "a2"})
}

func TestMultipleCommands(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a"})
	out := uc2Run(t, 0, "a", "arch", "a.txt", "&", "l", "arch")
	contains(t, out, "Compressing a.txt DONE", "\nListing files from arch.UC2\n", "A.TXT")
	if strings.Count(out, "Everything went OK") != 1 {
		t.Error("summary printed more than once")
	}
}

func randomData(n int, seed uint64) string {
	b := make([]byte, n)
	r := rand.New(rand.NewPCG(seed, 0))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return string(b)
}

func corrupt(t *testing.T, name string, off int64) {
	t.Helper()
	f, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, 3)
	f.ReadAt(b, off)
	for i := range b {
		b[i] ^= 0xA5
	}
	if _, err := f.WriteAt(b, off); err != nil {
		t.Fatal(err)
	}
}

func TestProtectAndRepair(t *testing.T) {
	files := map[string]string{"r1.bin": randomData(150000, 1), "r2.bin": randomData(30000, 2), "t.txt": "text"}
	setup(t, files)
	uc2Run(t, 0, "a", "arch", "*.*")
	uc2Run(t, 0, "P", "arch")
	contains(t, uc2Run(t, 0, "v", "arch"), "Archive is damage protected")
	contains(t, uc2Run(t, 0, "t", "arch"), "Testing archive sectors OK", "Verifying r1.bin OK")
	uc2Run(t, 0, "U", "arch")
	contains(t, uc2Run(t, 0, "v", "arch"), "Archive is NOT damage protected")
	uc2Run(t, 0, "ap", "arch") // nothing to add, but protection changes
	contains(t, uc2Run(t, 0, "v", "arch"), "Archive is damage protected")
	orig, _ := os.ReadFile("arch.UC2")

	for i, off := range []int64{70000, 5, -1} {
		os.WriteFile("bad.UC2", orig, 0o666)
		if off < 0 {
			// Damage the central directory, just before the protection area.
			off = 13 + int64(binary.LittleEndian.Uint32(orig[4:])) - 20
		}
		corrupt(t, "bad.UC2", off)
		out := uc2Run(t, sevDamaged, "T", "bad")
		contains(t, out, "Creating archive FIX_0001.UC2", "Archive has been repaired (using damage protection)",
			"MESSAGE: all files have been restored 100%", "1 error has been reported")
		fixed, _ := os.ReadFile("FIX_0001.UC2")
		if !bytes.Equal(fixed, orig) {
			t.Errorf("case %d: repaired archive differs from the original", i)
		}
		if b, _ := os.ReadFile("bad.UC2"); bytes.Equal(b, orig) {
			t.Errorf("case %d: original was modified", i)
		}
		os.Remove("FIX_0001.UC2")
	}
}

func TestSalvage(t *testing.T) {
	files := map[string]string{"a.txt": "small", "r.bin": randomData(100000, 3), "z.txt": strings.Repeat("z", 5000)}
	setup(t, files)
	uc2Run(t, 0, "a", "arch", "a.txt", "r.bin", "z.txt")
	corrupt(t, "arch.UC2", 50000)
	out := uc2Run(t, sevDamaged, "t", "arch")
	contains(t, out, "Archive is not damage protected", " ERROR 90: file r.bin is damaged", "Creating archive FIX_0001.UC2",
		"MESSAGE: some files might be damaged")
	checkContents(t, "FIX_0001.UC2", map[string]string{"a.txt": "small", "z.txt": files["z.txt"]})
	uc2Run(t, 0, "t", "FIX_0001")
	contains(t, uc2Run(t, sevDamaged, "e", "#out", "arch"), " ERROR 90: file r.bin is damaged")
	checkFile(t, "out/z.txt", files["z.txt"])
	notExist(t, "out/r.bin")
}

func TestExitCodes(t *testing.T) {
	setup(t, map[string]string{"junk.UC2": "not an archive", "UE2.UC2": "UE2 encrypted with UltraCrypt, long enough for a header"})
	contains(t, uc2Run(t, sevNoArchive, "l", "missing"), "FATAL ERROR 130: missing.UC2 does not exist")
	uc2Run(t, sevBroken, "l", "junk")
	uc2Run(t, sevEncrypted, "l", "UE2")
	contains(t, uc2Run(t, sevCmdLine, "d", "junk"), "FATAL ERROR 120: to delete all files *.* is needed")
	uc2Run(t, sevCmdLine, "q", "x")
	contains(t, uc2Run(t, sevNoMatch, "l", "none*"), "no archive found matching none*.UC2")
	// A fatal error stops the remaining commands.
	if out := uc2Run(t, sevNoArchive, "l", "missing", "&", "l", "junk"); strings.Contains(out, "junk") {
		t.Errorf("continued after a fatal error:\n%s", out)
	}
}

func TestOptimize(t *testing.T) {
	var sb strings.Builder
	for i := range 20000 {
		sb.WriteString("line ")
		sb.WriteString(strings.Repeat(string(rune('a'+i%26)), i%13))
		sb.WriteString(" of a moderately repetitive text\n")
	}
	setup(t, map[string]string{"t.txt": sb.String(), "u.txt": "u"})
	uc2Run(t, 0, "atf", "arch", "t.txt")
	uc2Run(t, 0, "ai", "arch", "u.txt")
	contains(t, uc2Run(t, 0, "o", "arch"), "Optimizing arch.UC2", "Old = ")
	checkContents(t, "arch", map[string]string{"t.txt": sb.String(), "u.txt": "u"})
	contains(t, uc2Run(t, sevNotSmaller, "o", "arch"), " WARNING 15: archive size has not changed")
}

func comment(t *testing.T, arch string) string {
	t.Helper()
	r, err := uc2.OpenReader(arch)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c, err := r.Comment()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestComment(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a", "c.txt": "line 1\nline 2\n", "empty.txt": ""})
	uc2Run(t, 0, "a", "arch", "a.txt")
	uc2Run(t, 0, "a", "arch2", "a.txt")
	uc2Run(t, 0, "r", "arch", "--comment-file", "c.txt")
	if c := comment(t, "arch.UC2"); c != "line 1\r\nline 2\r\n" {
		t.Errorf("comment %q", c)
	}
	if out := uc2Run(t, 0, "l", "arch"); strings.Contains(out, "U$~COMM") {
		t.Errorf("comment file listed:\n%s", out)
	}
	contains(t, uc2Run(t, 0, "l", "arch", "U$~COMM.TXT"), "U$~COMM.TXT")
	// Standard input is read once and applies to every archive.
	uc2Input(t, 0, "from stdin", "r", "arch", "arch2")
	for _, arch := range []string{"arch.UC2", "arch2.UC2"} {
		if c := comment(t, arch); c != "from stdin" {
			t.Errorf("%s: comment %q", arch, c)
		}
	}
	// Empty input keeps the comment; only an empty comment file removes it.
	contains(t, uc2Input(t, 0, "", "r", "arch"), "Comment is unchanged")
	if c := comment(t, "arch.UC2"); c != "from stdin" {
		t.Errorf("comment %q", c)
	}
	uc2Run(t, 0, "r", "arch", "--comment-file", "empty.txt")
	if c := comment(t, "arch.UC2"); c != "" {
		t.Errorf("comment %q", c)
	}
	checkContents(t, "arch", map[string]string{"a.txt": "a"})
}

func TestQuietVerbose(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a"})
	out := uc2Run(t, 0, "a", "-q", "arch", "a.txt")
	if out != "arch.UC2\nAdd a.txt\n" {
		t.Errorf("quiet output %q", out)
	}
	contains(t, uc2Run(t, 0, "--verbose", "a", "arch", "a.txt"), "UltraCompressor II Go port", "Smart skipping a.txt")
}

func TestUnrepairable(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a"})
	uc2Run(t, 0, "a", "arch", "a.txt")
	b, _ := os.ReadFile("arch.UC2")
	corrupt(t, "arch.UC2", int64(len(b))-30) // central directory
	contains(t, uc2Run(t, sevDamaged, "t", "arch"), " ERROR 90: archive arch.UC2 cannot be repaired")
	notExist(t, "FIX_0001.UC2")
	contains(t, uc2Run(t, sevBroken, "l", "arch"), "FATAL ERROR 200: archive arch.UC2 is damaged")
}

func TestDeviceNames(t *testing.T) {
	t.Chdir(t.TempDir())
	f, err := os.Create("dev.UC2")
	if err != nil {
		t.Fatal(err)
	}
	w := uc2.NewWriter(f)
	for _, name := range []string{"con.txt", "sub/a:b.txt", "lpt1/x"} {
		ew, err := w.CreateHeader(&uc2.FileHeader{Name: name, Modified: stamp})
		if err != nil {
			t.Fatal(err)
		}
		ew.Write([]byte(name))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if runtime.GOOS != "windows" {
		uc2Run(t, 0, "es", "dev")
		checkFile(t, "sub/a:b.txt", "sub/a:b.txt")
		return
	}
	contains(t, uc2Run(t, sevMapped, "es", "dev"), " WARNING 10: mapped (device)name con.txt to _con.txt",
		" WARNING 10: mapped (device)name "+disp("sub/a:b.txt", 0)+" to "+disp("sub/a_b.txt", 0))
	checkFile(t, "_con.txt", "con.txt")
	checkFile(t, "sub/a_b.txt", "sub/a:b.txt")
	checkFile(t, "_lpt1/x", "lpt1/x")
}

// writeRaw writes an archive holding data (at format.HeadSize) and the
// central directory c, for records the Writer does not create.
func writeRaw(t *testing.T, name string, data []byte, c *format.CDIR) {
	t.Helper()
	raw := c.Append(nil)
	stream := ultra.Compress(raw, ultra.NewDict(make([]byte, 512)), int(uc2.Tight))
	comp := format.Compress{CompLen: uint32(len(stream)), Method: uint16(uc2.Tight), Prefix: format.NoMaster}
	fh := format.FHead{CompLen: uint32(format.XHeadSize + len(data) + format.CompressSize + len(stream))}
	xh := format.XHead{Cdir: format.LocOf(int64(format.HeadSize + len(data))), Fletch: format.Fletch(raw), MadeBy: format.MadeBy, Needed: format.Needed}
	b := append(comp.Append(append(xh.Append(fh.Append(nil)), data...)), stream...)
	if err := os.WriteFile(name, fh.Append(b), 0o666); err != nil {
		t.Fatal(err)
	}
}

// readRaw returns the central directory of an archive.
func readRaw(t *testing.T, name string) *format.CDIR {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	xh, _ := format.ParseXHead(b[format.FHeadSize:])
	off := xh.Cdir.Abs()
	comp := format.ParseCompress(b[off:])
	start := off + format.CompressSize
	raw, err := io.ReadAll(ultra.NewReader(bytes.NewReader(b[start:start+int64(comp.CompLen)]), make([]byte, 512), ultra.Unlimited))
	if err != nil {
		t.Fatal(err)
	}
	c, err := format.ParseCDIR(raw, math.MaxInt)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func rawDir(parent, index uint32, short string, tags ...format.Tag) format.Entry {
	var n format.Name
	copy(n[:], fmt.Sprintf("%-11s", short))
	return format.Entry{Type: format.BoDir, Meta: format.Meta{Parent: parent, Attr: uint8(uc2.AttrDir), Name: n}, Index: index, Tags: tags}
}

func writeArchive(t *testing.T, arch string, names ...string) {
	t.Helper()
	f, err := os.Create(arch)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := uc2.NewWriter(f)
	for i, name := range names {
		ew, err := w.CreateHeader(&uc2.FileHeader{Name: name, Modified: stamp})
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(ew, "archived %d", i)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOptimizeKeepsTags(t *testing.T) {
	t.Chdir(t.TempDir())
	data := []byte(strings.Repeat("optimize me ", 2000))
	stream := ultra.Compress(data, ultra.NewDict(make([]byte, 512)), int(uc2.Fast))
	ea := format.Tag{Name: "OS/2 EA", Data: []byte("extended attributes")}
	e := format.Entry{Type: format.BoFile, Meta: format.Meta{Attr: uint8(uc2.AttrArchive)}, Size: uint32(len(data)), Fletch: format.Fletch(data),
		Comp: format.Compress{CompLen: uint32(len(stream)), Method: uint16(uc2.Fast), Prefix: format.NoMaster}, Loc: format.LocOf(format.HeadSize), Tags: []format.Tag{ea}}
	copy(e.Meta.Name[:], "EA      TXT")
	writeRaw(t, "ea.UC2", stream, &format.CDIR{Entries: []format.Entry{e}})
	uc2Run(t, 0, "op", "ea") // protection forces the rewrite
	checkContents(t, "ea", map[string]string{"EA.TXT": string(data)})
	for _, e := range readRaw(t, "ea.UC2").Entries {
		if e.Type != format.BoFile || e.Meta.Name.String() != "EA.TXT" {
			continue
		}
		if e.Comp.Method != uint16(uc2.Tight) {
			t.Errorf("method %d, not recompressed", e.Comp.Method)
		}
		if len(e.Tags) != 1 || e.Tags[0].Name != ea.Name || !bytes.Equal(e.Tags[0].Data, ea.Data) {
			t.Errorf("tags %q, want %q", e.Tags, ea)
		}
		return
	}
	t.Error("EA.TXT not found")
}

func TestMoveExtractRevisions(t *testing.T) {
	t.Chdir(t.TempDir())
	// Two revisions with the same size and time, so that only the name
	// tells which one is on disk.
	writeArchive(t, "arch.UC2", "f.txt", "f.txt")
	// Both revisions go to out/f.txt; only the one left on disk may leave the archive.
	uc2Run(t, 0, "emf", "#out", "arch", "f.txt;*")
	disk, _ := os.ReadFile(filepath.Join("out", "f.txt"))
	got := contents(t, "arch")
	if both := got["f.txt"] + string(disk); len(got) != 1 || both != "archived 0archived 1" && both != "archived 1archived 0" {
		t.Errorf("disk %q, archive %q", disk, got)
	}
}

func TestWildcardDirectories(t *testing.T) {
	setup(t, map[string]string{"test1.txt": "1", "other.txt": "o", "tests/a.go": "a", "tests/test2.txt": "2", "tests/sub/b.go": "b"})
	uc2Run(t, 0, "as", "arch", "*.*")
	uc2Run(t, 0, "as", "arch2", "test*")
	checkContents(t, "arch2", map[string]string{"test1.txt": "1", "tests/test2.txt": "2"})
	// A wildcard matching a directory does not select its subtree.
	uc2Run(t, 0, "es", "#x", "arch", "test*")
	checkFile(t, "x/test1.txt", "1")
	checkFile(t, "x/tests/test2.txt", "2")
	notExist(t, "x/tests/a.go")
	notExist(t, "x/tests/sub")
	contains(t, uc2Run(t, 0, "ls", "arch", "test*"), "files listed = 2 (2 bytes)")
	contains(t, uc2Run(t, 0, "vs", "arch", "test*"), "files listed           = 2 ")
	// Exclusions still exclude the subtrees of matching directories.
	uc2Run(t, 0, "es", "#y", "arch", "!test*")
	checkFile(t, "y/other.txt", "o")
	notExist(t, "y/tests")
	uc2Run(t, 0, "ds", "arch", "test*")
	checkContents(t, "arch", map[string]string{"other.txt": "o", "tests/a.go": "a", "tests/sub/b.go": "b"})
	// An exact name selects the subtree.
	uc2Run(t, 0, "ds", "arch", "tests")
	checkContents(t, "arch", map[string]string{"other.txt": "o"})
}

func TestAddSkipsInternalFiles(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a", "U$~COMM.TXT": "evil", "u$~12345.tmp": "junk", "sub/U$~X.TMP": "junk", "sub/b.txt": "b"})
	uc2Run(t, 0, "as", "arch", "*.*")
	checkContents(t, "arch", map[string]string{"a.txt": "a", "sub/b.txt": "b"})
	if c := comment(t, "arch.UC2"); c != "" {
		t.Errorf("comment %q", c)
	}
	uc2Run(t, 0, "a", "arch", "u$~comm.txt") // named exactly
	checkContents(t, "arch", map[string]string{"a.txt": "a", "sub/b.txt": "b", "U$~COMM.TXT": "evil"})
}

func TestIncrementalWriteFailure(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a"})
	uc2Run(t, 0, "a", "arch", "a.txt")
	orig, _ := os.ReadFile("arch.UC2")
	root, err := os.OpenRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fi, err := root.Lstat("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	// The invalid second name fails after the first file was appended.
	ad := &adder{a: newTestApp(""), c: &cmd{op: 'A', incremental: true}, items: []*item{
		{disk: "a.txt", root: root, rel: "a.txt", name: "b.txt", info: fi},
		{disk: "a.txt", root: root, rel: "a.txt", name: "c/../d.txt", info: fi},
	}}
	if err := ad.appendTo("arch.UC2"); code(err) != sevWrite {
		t.Fatalf("got %v, want write error", err)
	}
	if b, _ := os.ReadFile("arch.UC2"); !bytes.Equal(b, orig) {
		t.Error("failed update left data in the archive")
	}
	uc2Run(t, 0, "t", "arch")
}

func TestAddExcludeAncestor(t *testing.T) {
	setup(t, map[string]string{"src/proj/a.txt": "a", "src/proj/b.bak": "b"})
	// "src" names no file below the specification, only its ancestor.
	uc2Run(t, 0, "a", "arch", "src/proj/*.*", "-x", "src")
	checkContents(t, "arch", map[string]string{"a.txt": "a", "b.bak": "b"})
	uc2Run(t, 0, "a", "arch2", "src/proj/*.*", `!src\proj\b.bak`)
	checkContents(t, "arch2", map[string]string{"a.txt": "a"})
}

func TestAddConflictingNames(t *testing.T) {
	setup(t, map[string]string{"d1/x.txt": "1", "d2/X.TXT": "2", "y.txt": "y"})
	out := uc2Run(t, sevSkipped, "a", "arch", "d1/x.txt", "d2/X.TXT")
	contains(t, out, " WARNING 30: skipped file "+filepath.Join("d2", "X.TXT")+" (conflicting name)")
	checkContents(t, "arch", map[string]string{"x.txt": "1"})
	// Archive names match case-insensitively on every system.
	os.Chtimes(filepath.Join("d2", "X.TXT"), stamp.Add(time.Hour), stamp.Add(time.Hour))
	uc2Run(t, 0, "a", "arch", "d2/X.TXT")
	checkContents(t, "arch", map[string]string{"x.txt": "2"})
	// The same file twice is no conflict.
	uc2Run(t, 0, "a", "arch2", "y.txt", "*.txt")
}

func TestRewriteKeepsModeAndLink(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a", "b.txt": "b", "c.txt": "c", "real/": ""})
	arch := filepath.Join("real", "arch.UC2")
	uc2Run(t, 0, "a", arch, "*.txt")
	if runtime.GOOS != "windows" {
		os.Chmod(arch, 0o600)
		f, err := createTemp("real", 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		if fi, _ := os.Stat(f.Name()); fi.Mode().Perm() != 0o600 {
			t.Errorf("temporary file mode %v", fi.Mode())
		}
		os.Remove(f.Name())
		uc2Run(t, 0, "d", arch, "c.txt")
		if fi, _ := os.Stat(arch); fi.Mode().Perm() != 0o600 {
			t.Errorf("archive mode %v", fi.Mode())
		}
	}
	if err := os.Symlink(arch, "link.UC2"); err != nil {
		t.Skip("no symlinks:", err)
	}
	uc2Run(t, 0, "d", "link", "b.txt")
	if fi, err := os.Lstat("link.UC2"); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("link replaced: %v %v", fi, err)
	}
	want := map[string]string{"a.txt": "a"}
	if runtime.GOOS == "windows" {
		want["c.txt"] = "c"
	}
	checkContents(t, arch, want)
}

func TestListDuplicateDirectories(t *testing.T) {
	t.Chdir(t.TempDir())
	// Each level has two directories named "x" (distinct 8.3 names); only
	// the first has children. Visiting both would double the work per level.
	const depth = 20
	name := format.Tag{Name: "UC2X:UTF8Name", Data: []byte("x")}
	c := &format.CDIR{}
	for i := range uint32(depth) {
		parent := max(2*i, 1) - 1
		c.Entries = append(c.Entries, rawDir(parent, 2*i+1, "A", name), rawDir(parent, 2*i+2, "B", name))
	}
	writeRaw(t, "dup.UC2", nil, c)
	uc2Run(t, 0, "ls", "dup")
	if n := strings.Count(uc2Run(t, 0, "vs", "dup"), "--> Directory of"); n != depth+1 {
		t.Errorf("listed %d directories, want %d", n, depth+1)
	}
}

func TestHostileText(t *testing.T) {
	t.Chdir(t.TempDir())
	clean := func(out string) {
		t.Helper()
		for _, r := range out {
			if r < 0x20 && r != '\n' || r >= 0x7f && r < 0xa0 {
				t.Errorf("control character %U in output:\n%q", r, out)
				return
			}
		}
	}
	c := &format.CDIR{Entries: []format.Entry{
		rawDir(0, 1, "E\x1b[2J"),
		rawDir(0, 2, "F", format.Tag{Name: "UC2X:UTF8Name", Data: []byte("\x1b]0;pwn\x07\u009b\x7f")}),
	}}
	copy(c.Tail.Label[:], "\x1b[31mRED   ")
	writeRaw(t, "evil.UC2", nil, c)
	clean(uc2Run(t, 0, "l", "evil"))
	out := uc2Run(t, 0, "v", "evil")
	clean(out)
	contains(t, out, `archive volume label is "?[31mRED"`, "E?[2J")

	writeArchive(t, "names.UC2", "a\x1b[31m\u009b\x7f.txt")
	for _, args := range [][]string{{"l", "names"}, {"v", "names"}, {"t", "names"}, {"e", "#out", "names"}, {"d", "names", "*.*"}} {
		clean(uc2Run(t, 0, args...))
	}

	a := newTestApp("")
	a.warnf(sevSkipped, "file %s", "x\x1b[2Jy")
	a.errorf(sevWrite, "cannot write (%v)", errors.New("bad\u009bname"))
	if s := a.stderr.(*bytes.Buffer).String(); !strings.Contains(s, "file x?[2Jy\n") || !strings.Contains(s, "(bad?name)") {
		t.Errorf("messages not filtered: %q", s)
	}
}

// TestExtractPathSafety checks that no archive name leads outside the
// destination, not even names the library never returns.
func TestExtractPathSafety(t *testing.T) {
	a := newTestApp("")
	a.mapped = map[string]bool{}
	for _, rel := range []string{
		"../evil.txt", "a/../../evil.txt", "/evil.txt", `..\evil.txt`, `C:\evil.txt`, `\\?\C:\evil.txt`,
		`\\.\PhysicalDrive0`, "//server/share/evil.txt", "a/./b", "a\x00b", "",
	} {
		if target, ok := a.diskPath(rel); ok {
			t.Errorf("%q accepted as %q", rel, target)
		}
	}
	for _, rel := range []string{
		"C:evil.txt", "C:/evil.txt", "evil.txt:stream", "evil.txt::$DATA", "dir/evil. ", "...", " ..", "nul",
		"sub/COM1.txt", "COM¹", "CONOUT$", "a\x1bb",
	} {
		target, ok := a.diskPath(rel)
		if !ok || !filepath.IsLocal(filepath.FromSlash(target)) {
			t.Errorf("%q maps to %q (%v), which is not local", rel, target, ok)
		}
		if runtime.GOOS == "windows" && (strings.ContainsAny(target, `:\`) || target != mapPath(target)) {
			t.Errorf("%q maps to %q, which is not a plain Windows name", rel, target)
		}
	}
}

// TestExtractLinks checks that links inside the destination do not lead outside.
func TestExtractLinks(t *testing.T) {
	t.Chdir(t.TempDir())
	writeArchive(t, "links.UC2", "link/evil.txt", "junction/evil.txt", "a.txt")
	writeFiles(t, map[string]string{"outside/": "", "victim.txt": "victim", "out/": ""}, stamp)
	abs, _ := filepath.Abs("outside")
	links := 0
	if os.Symlink(filepath.Join("..", "outside"), filepath.Join("out", "link")) == nil &&
		os.Symlink(filepath.Join("..", "victim.txt"), filepath.Join("out", "a.txt")) == nil {
		links++
	}
	if runtime.GOOS == "windows" && exec.Command("cmd", "/c", "mklink", "/J", filepath.Join("out", "junction"), abs).Run() == nil {
		links++
	}
	if links == 0 {
		t.Skip("cannot create links")
	}
	uc2Run(t, sevWrite, "efs", "#out", "links")
	if es, _ := os.ReadDir("outside"); len(es) != 0 {
		t.Errorf("files created outside the destination: %v", es)
	}
	checkFile(t, "victim.txt", "victim")
	if fi, err := os.Lstat(filepath.Join("out", "a.txt")); err == nil && fi.Mode()&fs.ModeSymlink == 0 {
		checkFile(t, "out/a.txt", "archived 2")
	}
}
