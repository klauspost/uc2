package uc2

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/klauspost/uc2/internal/format"
	"github.com/klauspost/uc2/internal/ultra"
)

// rewriteCDIR returns archive b with its central directory modified by mod.
func rewriteCDIR(t *testing.T, b []byte, mod func(*format.CDIR)) []byte {
	t.Helper()
	r, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	mod(r.cdir)
	raw := r.cdir.Append(nil)
	cdirOff := r.xh.Cdir.Abs()
	stream := ultra.Compress(raw, zeroDict(), 3)
	out := append(bytes.Clone(b[:cdirOff]), format.Compress{CompLen: uint32(len(stream)), Method: 3, Prefix: format.NoMaster}.Append(nil)...)
	out = append(out, stream...)
	fh := format.FHead{CompLen: uint32(len(out) - format.FHeadSize)}
	xh := r.xh
	xh.Fletch = format.Fletch(raw)
	head := xh.Append(fh.Append(nil))
	copy(out, head)
	return append(out, head[:format.FHeadSize]...)
}

func archive(t *testing.T, files ...testFile) []byte {
	t.Helper()
	return writeArchive(t, files)
}

func TestCopyAliasCollision(t *testing.T) {
	a := archive(t, testFile{"Long file one.txt", []byte("one")})
	b := archive(t, testFile{"Long file two.txt", []byte("two")})
	var m memFile
	w := NewWriter(&m)
	for _, src := range [][]byte{a, b} {
		r, _ := NewReader(bytes.NewReader(src), int64(len(src)))
		for _, f := range r.File {
			if err := w.Copy(f); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := contents(t, m.b)
	if err != nil || c["Long file one.txt;0"] != "one" || c["Long file two.txt;0"] != "two" {
		t.Fatal(c, err)
	}
}

func TestCommentVsUserFile(t *testing.T) {
	var m memFile
	w := NewWriter(&m)
	fw, _ := w.Create("U$~COMM.TXT")
	io.WriteString(fw, "user data")
	w.SetComment("the comment")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, _ := NewReader(bytes.NewReader(m.b), int64(len(m.b)))
	if c, err := r.Comment(); err != nil || c != "the comment" {
		t.Fatal(c, err)
	}
	got, err := fs.ReadFile(r, "U$~COMM.TXT")
	if err != nil || string(got) != "user data" {
		t.Fatal(string(got), err)
	}
}

func TestDirWriter(t *testing.T) {
	var m memFile
	w := NewWriter(&m)
	dw, err := w.Create("dir/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dw.Write(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := dw.Write([]byte("x")); err == nil {
		t.Fatal("write to directory accepted")
	}
	w.Close()
}

func TestNegativeSize(t *testing.T) {
	if _, err := NewReader(bytes.NewReader(nil), -1); err == nil {
		t.Fatal("negative size accepted")
	}
}

func TestHostileNames(t *testing.T) {
	b := archive(t, testFile{"A.TXT", []byte("a")})
	b = rewriteCDIR(t, b, func(c *format.CDIR) {
		for i := range c.Entries {
			e := &c.Entries[i]
			if e.Type == format.BoFile && e.Meta.Name.String() == "A.TXT" {
				e.Tags = []format.Tag{{Name: tagUTF8Name, Data: []byte("\x1b[31m../evil\x07")}}
			}
		}
	})
	r, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.File {
		if strings.ContainsAny(f.Name, "\x1b\x07/") {
			t.Fatalf("unsanitized name %q", f.Name)
		}
	}
	// Oversized long names fall back to the 8.3 name.
	b = rewriteCDIR(t, b, func(c *format.CDIR) {
		for i := range c.Entries {
			if c.Entries[i].Type == format.BoFile {
				c.Entries[i].Tags = []format.Tag{{Name: tagUTF8Name, Data: bytes.Repeat([]byte("x"), maxNameLen+1)}}
			}
		}
	})
	r, _ = NewReader(bytes.NewReader(b), int64(len(b)))
	for _, f := range r.File {
		if f.rec.Type == format.BoFile && f.ShortName == "A.TXT" && f.Name != "A.TXT" {
			t.Fatalf("got %d byte name", len(f.Name))
		}
	}
}

func TestNameBudget(t *testing.T) {
	b := archive(t, testFile{"A.TXT", []byte("a")})
	b = rewriteCDIR(t, b, func(c *format.CDIR) {
		var dirs []format.Entry
		for i := range 3000 {
			n, _ := format.MakeName([]byte("D"), nil)
			dirs = append(dirs, format.Entry{Type: format.BoDir, Meta: format.Meta{Parent: uint32(i), Name: n}, Index: uint32(i + 1),
				Tags: []format.Tag{{Name: tagUTF8Name, Data: bytes.Repeat([]byte("n"), 4000)}}})
		}
		c.Entries = append(dirs, c.Entries...)
	})
	if _, err := NewReader(bytes.NewReader(b), int64(len(b))); !errors.Is(err, ErrFormat) {
		t.Fatal("expected a name budget error, got", err)
	}
}

func TestIndexFileDirConflict(t *testing.T) {
	b := archive(t, testFile{"X", []byte("file")}, testFile{"Y/Z", []byte("z")})
	b = rewriteCDIR(t, b, func(c *format.CDIR) {
		for i := range c.Entries {
			if c.Entries[i].Type == format.BoDir {
				c.Entries[i].Meta.Name, _ = format.MakeName([]byte("X"), nil)
			}
		}
	})
	r, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(r, "X/Z"); err != nil {
		t.Fatal(err)
	}
}

func TestTruncatedCopySource(t *testing.T) {
	b := archive(t, testFile{"A.TXT", bytes.Repeat([]byte("data "), 2000)})
	r, _ := NewReader(bytes.NewReader(b), int64(len(b)))
	var f *File
	for _, x := range r.File {
		if x.Name == "A.TXT" {
			f = x
		}
	}
	f.r.ra = &holeReader{b: b, from: f.offset + f.CompressedSize/2, to: f.offset + f.CompressedSize}
	var m memFile
	w := NewWriter(&m)
	err := w.Copy(f)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if !errors.Is(err, ErrFormat) {
		t.Fatal("copy of truncated data:", err)
	}
}

// holeReader behaves as if the data ended at from, for reads within [from, to).
type holeReader struct {
	b        []byte
	from, to int64
}

func (h *holeReader) ReadAt(p []byte, off int64) (int, error) {
	if off < h.to && off+int64(len(p)) > h.from {
		n := copy(p, h.b[off:max(off, h.from)])
		return n, io.EOF
	}
	return bytes.NewReader(h.b).ReadAt(p, off)
}

func TestAppendKeepsExtended(t *testing.T) {
	deep := strings.Repeat("directory/", 12) + "file.txt"
	b := archive(t, testFile{deep, []byte("x")})
	m := &memFile{b: bytes.Clone(b)}
	w, err := newAppendWriter(m, int64(len(m.b)), nil)
	if err != nil {
		t.Fatal(err)
	}
	fw, _ := w.Create("top.txt")
	fw.Write([]byte("y"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, _ := NewReader(bytes.NewReader(m.b), int64(len(m.b)))
	if r.xh.Needed != format.NeededLarge {
		t.Fatal("extended mode lost on append")
	}
}

func TestLargeFileExtended(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 2 GiB")
	}
	s := &sparseFile{}
	w := NewWriter(s, WithLevel(Fast))
	fw, _ := w.CreateHeader(&FileHeader{Name: "zeros", Modified: time.Now()})
	buf := make([]byte, 1<<20)
	const size = 2<<30 + 1
	for n := int64(0); n < size; n += int64(len(buf)) {
		fw.Write(buf[:min(int64(len(buf)), size-n)])
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(s, s.size)
	if err != nil || r.xh.Needed != format.NeededLarge {
		t.Fatal("files over 2 GiB need extended mode", err)
	}
}

func TestGenAlias(t *testing.T) {
	for long, want := range map[string]string{
		"readme.txt":          "README.TXT",
		"multi.part.name.txt": "MULTI~1.TXT",
		"my (file).txt":       "MY(FIL~1.TXT",
		"a+b.txt":             "A_B~1.TXT",
		".profile":            "PROFIL~1",
		"Café crème.txt":      "CAF\x90CR~1.TXT",
		"long name.txt":       "LONGNA~1.TXT",
		"con.txt":             "CON~1.TXT",
		"x.tar.gz":            "X~1.GZ",
		"U$~COMM.TXT":         "_$~COM~1.TXT",
	} {
		n := genAlias(long, CP437, func(format.Name) bool { return false }, map[string]int{})
		if string(n.Bytes()) != want {
			t.Errorf("%q: %q, want %q", long, n.Bytes(), want)
		}
	}
	// 0xE5 marks deleted directory entries; it is Õ in CP850.
	if n := genAlias("Õx.txt", CP850, func(format.Name) bool { return false }, map[string]int{}); string(n.Bytes()) != "_X~1.TXT" {
		t.Errorf("%q", n.Bytes())
	}
}

func TestLongNameLimits(t *testing.T) {
	long := strings.Repeat("é", 200) + ".txt" // 404 bytes UTF-8, 204 in CP437
	longer := strings.Repeat("x", 300)
	var m memFile
	w := NewWriter(&m)
	for _, n := range []string{long, longer, strings.Repeat("y", maxNameLen)} {
		fw, err := w.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write([]byte(n[:1]))
	}
	if _, err := w.Create(strings.Repeat("z", maxNameLen+1)); err == nil {
		t.Fatal("name over the limit accepted")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(bytes.NewReader(m.b), int64(len(m.b)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.File {
		if l := findTag(f.rec.Tags, tagLongName); len(l) > maxLongN+1 {
			t.Fatalf("LongN of %d bytes", len(l))
		}
		if len(f.Name) > 100 && f.Name != long && f.Name != longer && f.Name != strings.Repeat("y", maxNameLen) {
			t.Fatalf("name lost: %q...", f.Name[:20])
		}
	}
}

type grownInfo struct{ fs.FileInfo }

func (g grownInfo) Size() int64 { return g.FileInfo.Size() + 1 }

// changingFS reports a different size for bad once it is opened, as if it was
// replaced after the walk, and counts open files.
type changingFS struct {
	fstest.MapFS
	bad  string
	open atomic.Int32
}

func (c *changingFS) Open(name string) (fs.File, error) {
	f, err := c.MapFS.Open(name)
	if err != nil {
		return nil, err
	}
	c.open.Add(1)
	return &changingFile{f, c, name == c.bad}, nil
}

type changingFile struct {
	fs.File
	fs  *changingFS
	bad bool
}

func (f *changingFile) Stat() (fs.FileInfo, error) {
	fi, err := f.File.Stat()
	if f.bad {
		return grownInfo{fi}, err
	}
	return fi, err
}

func (f *changingFile) Close() error {
	f.fs.open.Add(-1)
	return f.File.Close()
}

func TestAddFSChanged(t *testing.T) {
	fsys := &changingFS{MapFS: fstest.MapFS{"a.txt": {Data: []byte("a")}, "b.txt": {Data: []byte("b")}}, bad: "b.txt"}
	for i := range 8 {
		fsys.MapFS[fmt.Sprintf("c%d.bin", i)] = &fstest.MapFile{Data: make([]byte, smallMax+1)}
	}
	var m memFile
	w := NewWriter(&m, WithConcurrency(2))
	if err := w.AddFS(fsys); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal(err)
	}
	if n := fsys.open.Load(); n != 0 {
		t.Fatalf("%d files left open", n)
	}
}

func fileEntries(c *format.CDIR) []*format.Entry {
	var files []*format.Entry
	for i := range c.Entries {
		if c.Entries[i].Type == format.BoFile {
			files = append(files, &c.Entries[i])
		}
	}
	return files
}

func TestReaderSpans(t *testing.T) {
	b := archive(t, testFile{"A.TXT", []byte("aaaa")}, testFile{"B.TXT", []byte("bbbb")})
	for name, mod := range map[string]func([]*format.Entry){
		"overlap":   func(e []*format.Entry) { e[1].Loc = e[0].Loc },
		"truncated": func(e []*format.Entry) { e[1].Comp.CompLen = 1 << 20 },
	} {
		bad := rewriteCDIR(t, b, func(c *format.CDIR) { mod(fileEntries(c)) })
		if _, err := NewReader(bytes.NewReader(bad), int64(len(bad))); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLocationOverflow(t *testing.T) {
	var m memFile
	w := NewWriter(&m)
	ew, _ := w.Create(strings.Repeat("directory/", 12) + "file.txt")
	ew.Write([]byte("deep"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b := rewriteCDIR(t, m.b, func(c *format.CDIR) { fileEntries(c)[0].Loc.Vol = 0x80000001 })
	if _, err := NewReader(bytes.NewReader(b), int64(len(b))); !errors.Is(err, ErrFormat) {
		t.Fatal(err)
	}
}

func TestReadCloseRace(t *testing.T) {
	b := archive(t, testFile{"A.BIN", bytes.Repeat([]byte("abcdefgh"), 1<<17)})
	r, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		rc, err := r.File[0].Open()
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Go(func() { io.Copy(io.Discard, rc) })
		rc.Close()
		wg.Wait()
	}
}

func TestSanitizeElement(t *testing.T) {
	for in, want := range map[string]string{
		"": "_", "..": "_", "a.": "a_", "a. ": "a__", "...": "___", "a/b": "a_b", "x\x1b": "x_",
		"GIT~1": "_GIT~1", ".git.": ".git_", ".GIT": ".GIT", ".gitignore": ".gitignore",
	} {
		if got := sanitizeElement(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestGenAliasTails(t *testing.T) {
	const n = 20000
	taken := map[format.Name]bool{}
	tails := map[string]int{}
	probes := 0
	for i := range n {
		a := genAlias(fmt.Sprintf("long file name %d.txt", i), CP437, func(a format.Name) bool { probes++; return taken[a] }, tails)
		if taken[a] || !safeAlias(a) {
			t.Fatalf("bad alias %q", a.Bytes())
		}
		taken[a] = true
	}
	if probes > 2*n {
		t.Fatalf("%d probes for %d names", probes, n)
	}
}

func TestAppendLock(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "netbsd", "openbsd", "dragonfly", "windows":
	default:
		t.Skip("no file locking on", runtime.GOOS)
	}
	name := filepath.Join(t.TempDir(), "a.uc2")
	if err := os.WriteFile(name, archive(t, testFile{"A.TXT", []byte("a")}), 0o644); err != nil {
		t.Fatal(err)
	}
	open := func() *os.File {
		f, err := os.OpenFile(name, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		return f
	}
	f1, f2 := open(), open()
	w, err := NewAppendWriter(f1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewAppendWriter(f2); !errors.Is(err, ErrLocked) {
		t.Fatal("second writer not locked out:", err)
	}
	fw, _ := w.Create("B.TXT")
	fw.Write([]byte("b"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if w, err = NewAppendWriter(f2); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
