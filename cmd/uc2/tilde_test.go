package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/klauspost/uc2"
	"github.com/klauspost/uc2/cmd/uc2/internal/term"
	"github.com/klauspost/uc2/internal/charset"
	"github.com/klauspost/uc2/internal/format"
)

// The files in testdata/tilde come from real UC2 runs in DOSBox-X: the
// archives T (made by UC2 revision 2), T237B (UC 2.37b), GOTAGS, CRAFT and
// CRAFT2 (made by this package, CRAFT with odd dates, attributes, tags and
// serial), the redirected output of UC r2 and 2.37b (ZZTilde*.bin, CP437
// with CRLF), their ~X dumps (*.DMP), ~R inputs with the ~X dumps of UC2's
// results (*-after-r2.X), and a ~V session (VIEW.TXT, V-*.txt).

var tildeData, _ = filepath.Abs("testdata/tilde")

// tildeDir copies the test archives, named as in the captures, into a new
// current directory and returns the directory of the test data.
func tildeDir(t *testing.T) string {
	t.Helper()
	data, oracle := tildeData, filepath.Join(tildeData, "..", "..", "..", "..", "testdata", "oracle")
	files := map[string]string{"REV.UC2": filepath.Join(oracle, "r2_rev.uc2"), "LFN.UC2": filepath.Join(oracle, "v237b_lfn.uc2"), "PROT.UC2": filepath.Join(oracle, "r2_prot.uc2")}
	for _, n := range []string{"T.UC2", "T237B.UC2", "GOTAGS.UC2", "CRAFT.UC2", "CRAFT2.UC2"} {
		files[n] = filepath.Join(data, n)
	}
	contents := map[string][]byte{}
	for dst, src := range files {
		var err error
		if contents[dst], err = os.ReadFile(src); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(t.TempDir())
	for n, b := range contents {
		if err := os.WriteFile(n, b, 0o666); err != nil {
			t.Fatal(err)
		}
	}
	return data
}

// capture returns UC2's redirected output as UTF-8 with LF line ends,
// without 2.37b's warning about the made up serial number of CRAFT.
func capture(t *testing.T, data, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(data, name))
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(charset.CP437.DecodeByte(c))
	}
	s := strings.ReplaceAll(sb.String(), "\r\n", "\n")
	return regexp.MustCompile(`(?m)^ WARNING 40: .*\n`).ReplaceAllString(s, "")
}

func tildeRun(t *testing.T, want int, args ...string) (stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	if got := run(args, strings.NewReader(""), &out, &errs); got != want {
		t.Fatalf("uc2 %q: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", args, got, want, out.String(), errs.String())
	}
	return out.String(), errs.String()
}

func exists(name string) bool {
	_, err := os.Lstat(name)
	return err == nil
}

var longNameLine = regexp.MustCompile(`(?m)^      LONGNAME=\[.*\]\r\n`)

func TestTildeD(t *testing.T) {
	data := tildeDir(t)
	for _, tc := range []struct{ capture, args string }{
		{"ZZTilde1-r2-O06.bin", `~D T`},
		{"ZZTilde1-r2-O06.bin", `~d T`}, // UC2: ~d t, but file names are case sensitive on Unix
		{"ZZTilde1-r2-O06.bin", `~D T.UC2 *.*;*`},
		{"ZZTilde1-r2-O06.bin", `~D -S T`},
		{"ZZTilde1-r2-O07.bin", `~D T *.TXT`},
		{"ZZTilde1-r2-O08.bin", `~D T BETA\*.*`},
		{"ZZTilde1-r2-O08.bin", `~D T BETA\`},
		{"ZZTilde1-r2-O09.bin", `~D T A.TXT;1`},
		{"ZZTilde1-r2-O11.bin", `~D T !*.TXT`},
		{"ZZTilde1-r2-O11.bin", `~D T *.*;* !*.TXT;*`},
		{"ZZTilde1-r2-O11.bin", `~D T B*.* !*.TXT`},
		{"ZZTilde1-r2-O13.bin", `~D T X*.*`},
		{"ZZTilde1-r2-O13.bin", `~D T *`},
		{"ZZTilde1-r2-O13.bin", `~D T SUB`},
		{"ZZTilde1-r2-O13.bin", `~D T NODIR\*.* X*.*`},
		{"ZZTilde1-r2-O14.bin", `~D REV`},
		{"ZZTilde1-2.37b-O15.bin", `~D LFN`},
		{"ZZTilde1-r2-O16.bin", `~D PROT`},
		{"ZZTilde1-2.37b-O17.bin", `~D GOTAGS`},
		{"ZZTilde1-2.37b-O06.bin", `~D T237B`},
		{"ZZTilde10-r2-O00.bin", `~D GOTAGS *.*`},
		{"ZZTilde10-r2-O00.bin", `~D GOTAGS *.*;*`},
		{"ZZTilde10-r2-O00.bin", `~D GOTAGS *.*;* !U$~COMM.TXT`},
		{"ZZTilde10-r2-O02.bin", `~D GOTAGS U$~COMM.TXT`},
		{"ZZTilde10-r2-O03.bin", `~D T A.TXT;*`},
		{"ZZTilde10-r2-O04.bin", `~D T *.TXT;1`},
		{"ZZTilde10-r2-O06.bin", `~D T BETA\INNER\*.*`},
		{"ZZTilde10-r2-O07.bin", `~D T NODIR\*.*`},
		{"ZZTilde10-r2-O08.bin", `~D T \BETA\B1.TXT;1`},
		{"ZZTilde10-r2-O09.bin", `~D T *.*;* !*.TXT`},
		{"ZZTilde8-2.37b-O00.bin", `~D CRAFT`},
		{"ZZTilde8-2.37b-O01.bin", `~D CRAFT2`},
		{"ZZTilde8-r2-O03.bin", `~D T BETA\*.* ALPHA\*.*`},
		{"ZZTilde8-r2-O05.bin", `~D T *.TXT *.BIN`},
		// Several archives, sorted, and several commands.
		{"ZZTilde1-r2-O06.bin ZZTilde1-2.37b-O06.bin", `~D T*.UC2`},
		{"ZZTilde1-r2-O06.bin ZZTilde1-r2-O14.bin", `~D T & ~D REV`},
	} {
		os.WriteFile(resultFile, []byte("old"), 0o666)
		got, errs := tildeRun(t, 0, strings.Fields(tc.args)...)
		var want string
		for c := range strings.FieldsSeq(tc.capture) {
			want += capture(t, data, c)
		}
		if strings.Contains(tc.capture, "-r2-") {
			// UC 2.37b added the long names; the r2 captures lack them.
			got = longNameLine.ReplaceAllString(got, "")
		}
		// 2.37b shows a name that CP437 cannot hold as stored in its long
		// name tag; the port shows the UTF-8 name.
		want = strings.Replace(want, "LONGNAME=[__.txt]", "LONGNAME=[日本.txt]", 1)
		want = strings.ReplaceAll(want, "\n", "\r\n")
		if got != want || errs != "" {
			t.Errorf("uc2 %s differs from UC2 (%s):\n%s\nstderr: %s", tc.args, tc.capture, lineDiff(want, got), errs)
		}
		if b, err := os.ReadFile(resultFile); err != nil || len(b) != 0 {
			t.Errorf("uc2 %s: %s %q, %v", tc.args, resultFile, b, err)
		}
	}
}

// lineDiff shows the first line where got differs from want.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := range max(len(w), len(g)) {
		if i >= len(w) || i >= len(g) || w[i] != g[i] {
			return fmt.Sprintf("line %d:\nwant %q\n got %q", i+1, w[min(i, len(w)-1)], g[min(i, len(g)-1)])
		}
	}
	return "equal"
}

func TestTildeErrors(t *testing.T) {
	tildeDir(t)
	os.WriteFile("T.TXT", nil, 0o666)
	for _, tc := range []struct {
		args string
		code int
		msg  string
	}{
		{`~D NOPE`, sevNoArchive, "FATAL ERROR 130: NOPE.UC2 does not exist"},
		{`~D`, sevCmdLine, "FATAL ERROR 120: no archive specified"},
		{`~D T *.* BETA\*.*`, sevCmdLine, "FATAL ERROR 120: double reference to single file, please simplify command line"},
		{`~DZ T`, sevCmdLine, "FATAL ERROR 120: unknown option -Z"},
		{`~X T T.DMP`, sevNoArchive, "FATAL ERROR 130: T does not exist"},
		{`~X`, sevCmdLine, "FATAL ERROR 120: no archive specified"},
		{`~X T`, sevCmdLine, "FATAL ERROR 120: no dumpfile specified"},
		{`~X T.UC2 X.DMP EXTRA`, sevCmdLine, "FATAL ERROR 120: unexpected parameter EXTRA"},
		{`~X T.TXT X.DMP`, sevBroken, "FATAL ERROR 200: you should repair this archive with 'uc2 T'"},
		{`~R T.UC2`, sevCmdLine, "FATAL ERROR 120: no dumpfile specified"},
		{`~K`, sevCmdLine, "FATAL ERROR 120: no path specified"},
		{`~K NOPE`, sevChdir, "FATAL ERROR 185: failed to change directory into NOPE"},
		{`~K T.UC2`, sevChdir, "FATAL ERROR 185: failed to change directory into T.UC2"},
		{`~V`, sevCmdLine, "FATAL ERROR 120: no file specified"},
		{`~V NOPE.TXT`, sevEditor, "FATAL ERROR 115: cannot locate file NOPE.TXT (for viewing)"},
		{`~V T.TXT`, sevEditor, "FATAL ERROR 115: cannot view T.TXT (not a terminal)"},
		{`~M EXTRA`, sevCmdLine, "FATAL ERROR 120: unexpected parameter EXTRA"},
		// Leading flags do not hide the command.
		{`--color=never ~X T`, sevCmdLine, "FATAL ERROR 120: no dumpfile specified"},
		{`--color never ~X T`, sevCmdLine, "FATAL ERROR 120: no dumpfile specified"},
	} {
		os.WriteFile(resultFile, nil, 0o666)
		out, errs := tildeRun(t, tc.code, strings.Fields(tc.args)...)
		// Dump mode: no logo, no summary, and U$~RESLT.OK is removed.
		if !strings.HasSuffix(errs, "\n"+tc.msg+"\n") || strings.Contains(errs, "reported") || out != "" || exists(resultFile) {
			t.Errorf("uc2 %s:\nstdout %q\nstderr %q\n%s exists: %v", tc.args, out, errs, resultFile, exists(resultFile))
		}
	}
	// Other ~ commands end as usual, without the logo.
	os.WriteFile(resultFile, nil, 0o666)
	for _, tc := range []struct {
		args      string
		code      int
		out, errs string
	}{
		{`~Q T`, sevCmdLine, "", "\nFATAL ERROR 120: unknown command ~Q\n\n1 error has been reported \n"},
		{`~ T`, sevCmdLine, "", "\nFATAL ERROR 120: unknown command ~\n\n1 error has been reported \n"},
		{`~~123`, 0, "\nEverything went OK\n", ""},
	} {
		if out, errs := tildeRun(t, tc.code, strings.Fields(tc.args)...); out != tc.out || errs != tc.errs || !exists(resultFile) {
			t.Errorf("uc2 %s: stdout %q, stderr %q", tc.args, out, errs)
		}
	}
}

func TestTildeDuplicateDirs(t *testing.T) {
	// Of two directories of one name, UC2 finds the later one, which it
	// put in front of the other.
	t.Chdir(t.TempDir())
	writeRaw(t, "DUP.UC2", nil, &format.CDIR{Entries: []format.Entry{rawDir(0, 1, "D"), rawDir(1, 3, "S1"), rawDir(0, 2, "D"), rawDir(2, 4, "S2")}})
	if out, _ := tildeRun(t, 0, "~D", "DUP", `D\*.*`); !strings.Contains(out, "NAME=[S2]") || strings.Contains(out, "S1") {
		t.Errorf("~D DUP D\\*.*:\n%s", out)
	}
}

func TestTildeResult(t *testing.T) {
	tildeDir(t)
	os.Remove(resultFile)
	if out, errs := tildeRun(t, 0, "~M"); out != "" || errs != "" || !exists(resultFile) {
		t.Errorf("~M: %q %q", out, errs)
	}
	os.Remove(resultFile)
	tildeRun(t, 0, "~~")
	if exists(resultFile) {
		t.Errorf("~~ created %s", resultFile)
	}
	// A dump command anywhere selects the dump mode end; the logo depends
	// on the first command.
	out, _ := tildeRun(t, 0, "L", "T", "&", "~M")
	if !strings.HasPrefix(out, "═") || strings.Contains(out, "Everything went OK") || !exists(resultFile) {
		t.Errorf("L & ~M:\n%s", out)
	}
	// The minimal output level stays for later commands.
	if out, _ := tildeRun(t, 0, "~D", "T", "&", "L", "T"); strings.Contains(out, "Listing files from") || !strings.Contains(out, "END\r\nT.UC2\n") {
		t.Errorf("~D & L:\n%s", out)
	}
	t.Setenv("UC2_OK", "off")
	os.WriteFile(resultFile, []byte("keep"), 0o666)
	tildeRun(t, 0, "~D", "T")
	if b, _ := os.ReadFile(resultFile); string(b) != "keep" {
		t.Errorf("UC2_OK=off: %s is %q", resultFile, b)
	}
	tildeRun(t, sevNoArchive, "~D", "NOPE")
	if exists(resultFile) {
		t.Errorf("UC2_OK=off: %s kept after an error", resultFile)
	}
	os.Remove(resultFile)
	tildeRun(t, 0, "~D", "T")
	if exists(resultFile) {
		t.Errorf("UC2_OK=off: %s created", resultFile)
	}
	// Interrupts remove it too.
	a := newTestApp("")
	a.dump = true
	os.WriteFile(resultFile, nil, 0o666)
	a.abort()
	if exists(resultFile) || strings.Contains(a.stderr.(*bytes.Buffer).String(), "reported") {
		t.Errorf("after an interrupt: %q", a.stderr)
	}
}

// maskGarbage zeroes the bytes after the NUL of each tag name of a dump,
// which UC2 fills with whatever was on its stack.
func maskGarbage(d []byte) []byte {
	d = bytes.Clone(d)
	for i := 0; i+2 <= len(d); {
		l := int(binary.LittleEndian.Uint16(d[i:]))
		if i += 2; l == 0 {
			break
		}
		for i += l & 0x7FFF; i < len(d); {
			op := d[i]
			i++
			if op == 0 {
				break
			}
			if op == 2 {
				clear(d[i+bytes.IndexByte(d[i:i+16], 0) : i+16])
				i += 20 + int(binary.LittleEndian.Uint32(d[i+16:]))
			}
		}
	}
	return d
}

func TestTildeX(t *testing.T) {
	data := tildeDir(t)
	for arch, dump := range map[string]string{"T.UC2": "T.DMP", "LFN.UC2": "LFN.DMP", "REV.UC2": "REV.DMP", "PROT.UC2": "PROT.DMP", "GOTAGS.UC2": "GOTAGS-r2.DMP", "CRAFT.UC2": "CRAFT.DMP"} {
		os.WriteFile("OUT.DMP", make([]byte, 10000), 0o666) // truncated
		if out, errs := tildeRun(t, 0, "~X", arch, "OUT.DMP"); out != "" || errs != "" {
			t.Errorf("~X %s: %q %q", arch, out, errs)
		}
		got, _ := os.ReadFile("OUT.DMP")
		want, err := os.ReadFile(filepath.Join(data, dump))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, maskGarbage(want)) {
			t.Errorf("~X %s differs from UC2's %s:\n got % x\nwant % x", arch, dump, got, maskGarbage(want))
		}
	}
	before, _ := os.ReadFile("T.UC2")
	_, errs := tildeRun(t, sevCmdLine, "~X", "T.UC2", "T.UC2")
	if after, _ := os.ReadFile("T.UC2"); !bytes.Equal(before, after) || !strings.Contains(errs, "the dumpfile T.UC2 is the archive") {
		t.Errorf("dump over the archive: %q", errs)
	}
	_, errs = tildeRun(t, sevWrite, "~X", "T.UC2", filepath.Join("NODIR", "X.DMP"))
	contains(t, errs, "FATAL ERROR 80: cannot write "+filepath.Join("NODIR", "X.DMP"))
}

// dumpTags returns the tags each record of a dump lists.
func dumpTags(t *testing.T, name string) map[string][]format.Tag {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := parseDump(b)
	if err != nil {
		t.Fatal(name, err)
	}
	m := map[string][]format.Tag{}
	for _, r := range recs {
		l := m[string(r.path)]
		for _, op := range r.ops {
			if op == nil {
				l = nil
			} else {
				l = append(l, *op)
			}
		}
		m[string(r.path)] = l
	}
	return m
}

// sameTags compares tag lists, the managed ones regardless of their order:
// the port keeps them first, and UC2 reversed the lists it did not change.
func sameTags(a, b []format.Tag) bool {
	split := func(l []format.Tag) (own, man []string) {
		for _, x := range l {
			s := x.Name + "=" + string(x.Data)
			if len(managed([]format.Tag{x})) > 0 {
				man = append(man, s)
			} else {
				own = append(own, s)
			}
		}
		slices.Sort(man)
		return own, man
	}
	ao, am := split(a)
	bo, bm := split(b)
	return slices.Equal(ao, bo) && slices.Equal(am, bm)
}

func TestTildeR(t *testing.T) {
	// UC2's own dumps come back without changing anything, also those with
	// directory tags, on which UC2 hangs.
	data := tildeDir(t)
	for arch, dump := range map[string]string{"T.UC2": "T.DMP", "LFN.UC2": "LFN.DMP", "REV.UC2": "REV.DMP", "PROT.UC2": "PROT.DMP", "GOTAGS.UC2": "GOTAGS-r2.DMP", "CRAFT.UC2": "CRAFT.DMP"} {
		before, _ := os.ReadFile(arch)
		os.Remove(resultFile)
		if out, errs := tildeRun(t, 0, "~R", arch, filepath.Join(data, dump)); out != "" || errs != "" || !exists(resultFile) {
			t.Errorf("~R %s %s: %q %q", arch, dump, out, errs)
		}
		if after, _ := os.ReadFile(arch); !bytes.Equal(before, after) {
			t.Errorf("~R %s %s changed the archive", arch, dump)
		}
	}

	// The tags after UC2's successful ~R runs.
	for _, tc := range []struct{ id, arch string }{
		{"R1", "T"}, {"R8", "T"}, {"R15", "T"}, {"R16", "T"}, {"R17", "PROT"}, {"R20", "GOTAGS"}, {"R21", "T"}, {"R22", "T"}, {"L5", "LFN"},
	} {
		tildeDir(t)
		tildeRun(t, 0, "~R", tc.arch+".UC2", filepath.Join(data, tc.id+".DMP"))
		tildeRun(t, 0, "~X", tc.arch+".UC2", "GOT.DMP")
		got, want := dumpTags(t, "GOT.DMP"), dumpTags(t, filepath.Join(data, tc.id+"-after-r2.X"))
		for p, w := range want {
			if g, ok := got[p]; !ok || !sameTags(g, w) {
				t.Errorf("~R %s: %s has %v, UC2 %v", tc.id, p, g, w)
			}
		}
		if len(got) != len(want) {
			t.Errorf("~R %s: %d entries, UC2 %d", tc.id, len(got), len(want))
		}
		uc2Run(t, 0, "T", tc.arch)
	}
	r, err := uc2.OpenReader("PROT.UC2")
	if err != nil || !r.Protected || r.Check() != nil {
		t.Errorf("~R lost the damage protection: %v", err)
	}
	r.Close()

	// Where UC2 fails, differs or does nothing.
	big := bytes.Repeat([]byte("0123456789"), 150)
	for _, tc := range []struct {
		id, path string
		tags     []format.Tag
	}{
		{"R12", "B.BIN", []format.Tag{{Name: "TEST:B", Data: []byte("bin")}}}, // no terminator
		{"R13", "B.BIN", nil}, // empty
		{"R14", `BETA\`, []format.Tag{{Name: "TEST:Dir", Data: []byte("dir")}}},
		{"R19", "B.BIN", []format.Tag{{Name: "TEST:Big", Data: big}}}, // UC2 garbles tags over 1000 bytes
	} {
		tildeDir(t)
		tildeRun(t, 0, "~R", "T.UC2", filepath.Join(data, tc.id+".DMP"))
		tildeRun(t, 0, "~X", "T.UC2", "GOT.DMP")
		if g := dumpTags(t, "GOT.DMP")[tc.path]; !sameTags(g, tc.tags) {
			t.Errorf("~R %s: %s has %v", tc.id, tc.path, g)
		}
	}
}

// makeDump returns a tag dump that adds tags to the entry at path.
func makeDump(path string, tags ...format.Tag) []byte {
	d := binary.LittleEndian.AppendUint16(nil, 0x8000|uint16(len(path)+1))
	d = append(append(d, path...), 0)
	for _, x := range tags {
		var name [16]byte
		copy(name[:], x.Name)
		d = binary.LittleEndian.AppendUint32(append(append(d, 2), name[:]...), uint32(len(x.Data)))
		d = append(d, x.Data...)
	}
	return append(d, 0, 0, 0)
}

func TestTildeRErrors(t *testing.T) {
	data := tildeDir(t)
	dmp := func(id string) string { return filepath.Join(data, id+".DMP") }
	before, _ := os.ReadFile("T.UC2")
	os.WriteFile("REV2.DMP", makeDump("A.TXT;2", format.Tag{Name: "TEST:A"}), 0o666)
	// Tags of zeros compress too well: the reader would refuse the result.
	zeros := make([]byte, format.MaxTagSize)
	os.WriteFile("BIG.DMP", makeDump("A.TXT", format.Tag{Name: "TEST:A", Data: zeros}, format.Tag{Name: "TEST:B", Data: zeros}), 0o666)
	for _, tc := range []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"T.UC2", dmp("R5")}, sevCmdLine, "names NOPE.TXT, which is not in T.UC2"},
		{[]string{"T.UC2", dmp("R6")}, sevCmdLine, `names NODIR\X.TXT, which is not in T.UC2`},
		{[]string{"T.UC2", dmp("R7")}, sevCmdLine, "names A.TXT;5, which is not in T.UC2"},
		{[]string{"T.UC2", "REV2.DMP"}, sevCmdLine, "names A.TXT;2, which is not in T.UC2"},
		{[]string{"T.UC2", dmp("R9")}, sevCmdLine, "names a.txt, which is not in T.UC2"},
		{[]string{"T.UC2", dmp("R10")}, sevCmdLine, "is invalid (invalid tag name at offset 8)"},
		{[]string{"T.UC2", dmp("R11")}, sevCmdLine, "is invalid (unknown operation 3 at offset 8)"},
		{[]string{"T.UC2", "NOPE.DMP"}, sevCmdLine, "FATAL ERROR 120: cannot read dumpfile NOPE.DMP ("},
		{[]string{"T", dmp("R1")}, sevNoArchive, "FATAL ERROR 130: T does not exist"},
		{[]string{"T.UC2", "BIG.DMP"}, sevWrite, "FATAL ERROR 80: cannot write T.UC2 (uc2: central directory too large"},
	} {
		os.WriteFile(resultFile, nil, 0o666)
		_, errs := tildeRun(t, tc.code, append([]string{"~R"}, tc.args...)...)
		contains(t, errs, tc.msg)
		if after, _ := os.ReadFile("T.UC2"); !bytes.Equal(before, after) || exists(resultFile) {
			t.Errorf("~R %s changed the archive, or left %s", tc.args, resultFile)
		}
	}

	// Long name and size tags stay; a changed long name would break the
	// alias, which UC 2.37b depends on.
	lfn, _ := os.ReadFile("LFN.UC2")
	for id, want := range map[string][]string{"L1": {"ALONGF~1.TXT"}, "L2": {"LOWER.TXT"}, "R4": {"ALONGF~1.TXT", "LOWER.TXT"}} {
		_, errs := tildeRun(t, sevSkipped, "~R", "LFN.UC2", dmp(id))
		var msgs []string
		for _, n := range want {
			msgs = append(msgs, " WARNING 30: long name and size tags of "+n+" are managed and were not changed\n")
		}
		if errs != strings.Join(msgs, "") {
			t.Errorf("~R LFN %s: %q", id, errs)
		}
		if after, _ := os.ReadFile("LFN.UC2"); !bytes.Equal(lfn, after) {
			t.Errorf("~R LFN %s changed the archive", id)
		}
	}

	// The archive is locked and checked like for other updates.
	f, err := os.OpenFile("T.UC2", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	w, err := uc2.NewAppendWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	_, errs := tildeRun(t, sevWrite, "~R", "T.UC2", dmp("R1"))
	contains(t, errs, "FATAL ERROR 80: cannot update T.UC2 (it is being updated by another process)")
	w.Close()
	f.Close()
	b, _ := os.ReadFile("PROT.UC2")
	b[len(b)/3] ^= 0xFF
	os.WriteFile("PROT.UC2", b, 0o666)
	_, errs = tildeRun(t, sevBroken, "~R", "PROT.UC2", dmp("R17"))
	contains(t, errs, " ERROR 90: archive PROT.UC2 is damaged", "FATAL ERROR 200")
	if after, _ := os.ReadFile("PROT.UC2"); !bytes.Equal(b, after) {
		t.Error("~R changed a damaged archive")
	}
}

// link makes a link to a directory, a junction on Windows, where symbolic
// links need privileges.
func link(t *testing.T, target, name string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		abs, _ := filepath.Abs(target)
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", name, abs).CombinedOutput(); err != nil {
			t.Skipf("mklink /J: %v %s", err, out)
		}
		return
	}
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func TestTildeK(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFiles(t, map[string]string{
		"KT/A.TXT": "a", "KT/B.U~K": "kept", "KT/RO.TXT": "ro", "KT/SUB/X.TXT": "x", "KT/SUB/DEEP/Y.TXT": "y",
		"KT/KEEP.U~K/Z.TXT": "kept", "KT/EMPTY/": "", "KT/lower.u~k.txt": "kept",
		"OUT/SAFE.TXT": "outside", "OUT/SUB/SAFE.TXT": "outside",
		"KT2/A.TXT": "a", "KT2/SUB/S.U~K": "kept", "KT2/SUB/T.TXT": "t", "KT2/Z.TXT": "z",
	}, stamp)
	os.Chmod(filepath.Join("KT", "RO.TXT"), 0o444)
	os.Chmod(filepath.Join("KT", "SUB", "DEEP", "Y.TXT"), 0o444)
	link(t, "OUT", filepath.Join("KT", "LINK"))
	link(t, filepath.Join("..", "..", "OUT"), filepath.Join("KT", "SUB", "UP"))
	if runtime.GOOS != "windows" {
		os.Symlink(filepath.Join("..", "OUT", "SAFE.TXT"), filepath.Join("KT", "FLINK"))
	}
	if out, errs := tildeRun(t, 0, "~K", "KT"); out != "" || errs != "" {
		t.Errorf("~K KT: %q %q", out, errs)
	}
	var left []string
	filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if p != "." {
			left = append(left, filepath.ToSlash(p))
		}
		return err
	})
	want := []string{"KT", "KT/B.U~K", "KT/KEEP.U~K", "KT/KEEP.U~K/Z.TXT", "KT/lower.u~k.txt",
		"KT2", "KT2/A.TXT", "KT2/SUB", "KT2/SUB/S.U~K", "KT2/SUB/T.TXT", "KT2/Z.TXT",
		"OUT", "OUT/SAFE.TXT", "OUT/SUB", "OUT/SUB/SAFE.TXT", resultFile}
	if !slices.Equal(left, want) {
		t.Errorf("left after ~K KT:\n%s", strings.Join(left, "\n"))
	}

	// A directory that keeps a .U~K entry is reported once; the rest goes.
	_, errs := tildeRun(t, sevRmdir, "~K", "KT2")
	if n := strings.Count(errs, " ERROR 60: failed to delete directory SUB ("); n != 1 || strings.Count(errs, "ERROR") != 1 {
		t.Errorf("~K KT2: %q", errs)
	}
	if !exists(filepath.Join("KT2", "SUB", "S.U~K")) || exists(filepath.Join("KT2", "SUB", "T.TXT")) || exists(filepath.Join("KT2", "Z.TXT")) || exists(resultFile) {
		t.Error("~K KT2 did not delete the rest")
	}

	// ~K . empties the current directory.
	t.Chdir("KT")
	tildeRun(t, 0, "~K", ".")
	if l, _ := os.ReadDir("."); len(l) != 4 { // B.U~K, KEEP.U~K, lower.u~k.txt and the result
		t.Errorf("~K . left %v", l)
	}
}

// snapColors are the SGR attributes of the colors in the screen captures.
var snapColors = map[string]string{"Black/LightGray": "30;47", "Red/LightGray": "31;47", "White/Blue": "97;44", "LightCyan/Blue": "96;44", "LightGray": ""}

// snapshots reads the screens of a capture as markup like vt.markups.
func snapshots(t *testing.T, name string) [][]string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var snaps [][]string
	for l := range strings.SplitSeq(strings.ReplaceAll(string(b), "\r", ""), "\n") {
		switch {
		case strings.HasPrefix(l, "======== snapshot"):
			snaps = append(snaps, nil)
		case strings.HasPrefix(l, "  |"):
			var sb strings.Builder
			cur := ""
			for s := l[3:]; s != ""; {
				if s[0] == '{' {
					i := strings.IndexByte(s, '}')
					if c := snapColors[s[1:i]]; c != cur {
						cur = c
						sb.WriteString("{" + c + "}")
					}
					s = s[i+1:]
					continue
				}
				_, n := utf8.DecodeRuneInString(s)
				sb.WriteString(s[:n])
				s = s[n:]
			}
			snaps[len(snaps)-1] = append(snaps[len(snaps)-1], strings.TrimRight(sb.String(), " "))
		}
	}
	return snaps
}

// TestTildeV replays a ~V session of UC2 revision 2 and compares the
// screens, apart from the "+ 50 lines" key, which the port does not have.
func TestTildeV(t *testing.T) {
	data := tildeDir(t)
	b, _ := os.ReadFile(filepath.Join(data, "VIEW.TXT"))
	// UC2 dropped the last line, which has no line end; the port shows it.
	os.WriteFile("README.TXT", b[:bytes.LastIndexByte(b, '\n')+1], 0o666)
	snaps := snapshots(t, filepath.Join(data, "V-esc-snapshots-r2.txt"))
	var v *vt
	var a *app
	i := 0
	check := func() {
		want := snaps[i]
		want[23] = strings.Replace(want[23], "{31;47}+ {30;47}50 lines  ", "", 1)
		if got := v.markups(); !slices.Equal(got, want) {
			for y := range want {
				if got[y] != want[y] {
					t.Errorf("snapshot %d, row %d:\n got %s\nwant %s", i, y, got[y], want[y])
				}
			}
		}
		i++
	}
	a, v = helpApp(t, 80, 25, check, "b", check, term.KeyPgDn, check, "3", check, "s", check, term.KeyHome, check, "z", check, term.KeyEsc)
	os.Remove(resultFile)
	runHelp(t, a, v, "~V", "README.TXT")
	if i != len(snaps) || strings.Join(v.lines(), "") != "" || !exists(resultFile) {
		t.Errorf("%d of %d snapshots; after Esc:\n%s", i, len(snaps), strings.Join(v.lines(), "\n"))
	}

	// Tab leaves the summary as UC2 did (V-tab-final-r2.txt), without
	// "Everything went OK".
	a, v = helpApp(t, 80, 25, term.KeyTab)
	runHelp(t, a, v, "~V", "README.TXT")
	if got := v.markups()[:4]; !slices.Equal(got, []string{"{97;44}1.Z SUMMARY.", "{97;44}============", "{96;44}Summary line 1.", ""}) || v.y != 3 {
		t.Errorf("after Tab, cursor on row %d:\n%s", v.y, strings.Join(got, "\n"))
	}
}
