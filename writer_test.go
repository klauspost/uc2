package uc2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/klauspost/uc2/internal/format"
	"github.com/klauspost/uc2/internal/ultra"
)

// memFile is an in-memory io.WriteSeeker.
type memFile struct {
	b   []byte
	pos int
}

func (m *memFile) Write(p []byte) (int, error) {
	if need := m.pos + len(p); need > len(m.b) {
		m.b = append(m.b, make([]byte, need-len(m.b))...)
	}
	copy(m.b[m.pos:], p)
	m.pos += len(p)
	return len(p), nil
}

func (m *memFile) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		off += int64(m.pos)
	case io.SeekEnd:
		off += int64(len(m.b))
	}
	m.pos = int(off)
	return off, nil
}

type testFile struct {
	name string
	data []byte
}

func corpus(seed uint64) []testFile {
	rng := rand.New(rand.NewPCG(seed, 7))
	text := func(n int) []byte {
		words := []string{"alpha", "beta", "gamma", "delta", "UltraCompressor", "master", "\r\n", "int main(void) {", "}"}
		var b bytes.Buffer
		for b.Len() < n {
			b.WriteString(words[rng.IntN(len(words))])
			b.WriteByte(' ')
		}
		return b.Bytes()[:n]
	}
	random := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(rng.Uint32())
		}
		return b
	}
	wav := make([]byte, 60000)
	for i := 0; i < len(wav); i += 2 {
		binary.LittleEndian.PutUint16(wav[i:], uint16(int16(9000*math.Sin(float64(i)/30))))
	}
	return []testFile{
		{"README.TXT", text(5000)},
		{"readme.md", text(3000)},
		{"src/main.c", text(12000)},
		{"src/util.c", text(9000)},
		{"src/util.h", text(800)},
		{"src/very long file name.c", text(4000)},
		{"src/Ärger ümlaut.txt", text(100)},
		{"src/日本語.txt", text(50)},
		{"bin/data.bin", random(20000)},
		{"bin/empty.dat", nil},
		{"bin/tiny", []byte("x")},
		{"media/tone.wav", wav},
		{"deep/a/b/c/d/e/f/g/h/i/j/k/file.txt", text(700)},
		{"con.txt", []byte("device")},
		{"big/big.bin", append(text(1500000), random(700000)...)},
	}
}

func writeArchive(t testing.TB, files []testFile, opts ...Option) []byte {
	t.Helper()
	var m memFile
	w := NewWriter(&m, opts...)
	mod := time.Date(2001, 2, 3, 4, 5, 6, 0, time.Local)
	for _, f := range files {
		ew, err := w.CreateHeader(&FileHeader{Name: f.name, Modified: mod, Attr: AttrArchive})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ew.Write(f.data); err != nil {
			t.Fatal(err)
		}
	}
	w.SetComment("Hello from Go")
	w.SetLabel("GOLABEL")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return m.b
}

// verifyArchive checks that b contains files (newest revisions), passes the
// strict stream checks and the r2 central directory invariants.
func verifyArchive(t testing.TB, b []byte, files []testFile) *Reader {
	t.Helper()
	r, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Check(); err != nil {
		t.Fatal("check:", err)
	}
	if err := r.cdir.Validate(r.extended); err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{}
	for _, f := range files {
		want[f.name] = f.data
	}
	seen := 0
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "/") || f.Revision != 0 || strings.HasPrefix(f.ShortName, "U$~") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		if w, ok := want[f.Name]; !ok || !bytes.Equal(got, w) {
			t.Fatalf("%s: content mismatch (known %v, %d vs %d bytes)", f.Name, ok, len(got), len(w))
		}
		seen++
		m := r.masters[f.rec.Comp.Prefix]
		dict, _ := r.master(m.Index)
		delta, _, _ := format.MethodInfo(f.rec.Comp.Method)
		stream := make([]byte, f.CompressedSize)
		r.ra.ReadAt(stream, f.offset)
		if delta > 0 {
			dl := ultra.NewDelta(delta)
			dict = bytes.Clone(dict)
			dl.Encode(dict)
		}
		if err := ultra.Strict(stream, dict, f.Size); err != nil {
			t.Fatalf("%s: strict: %v", f.Name, err)
		}
	}
	for _, m := range r.masters {
		stream := make([]byte, m.Comp.CompLen)
		off, _ := r.abs(m.Loc)
		r.ra.ReadAt(stream, off)
		dict, _ := r.dict(m.Comp.Prefix)
		if err := ultra.Strict(stream, dict, int64(m.Len)); err != nil {
			t.Fatalf("master %d: strict: %v", m.Index, err)
		}
	}
	if seen != len(want) {
		t.Fatalf("found %d of %d files", seen, len(want))
	}
	return r
}

func TestWriterRoundTrip(t *testing.T) {
	files := corpus(1)
	for _, l := range []Level{Fast, Normal, Tight, SuperTight} {
		t.Run(fmt.Sprint(l), func(t *testing.T) {
			b := writeArchive(t, files, WithLevel(l))
			r := verifyArchive(t, b, files)
			if c, err := r.Comment(); err != nil || c != "Hello from Go" || r.Label != "GOLABEL" {
				t.Fatalf("comment %q %v label %q", c, err, r.Label)
			}
			if r.xh.Needed != format.Needed {
				t.Fatal("unexpected extended mode")
			}
			t.Logf("level %d: %d bytes, %d masters", l, len(b), len(r.masters))
		})
	}
}

func TestWriterProtected(t *testing.T) {
	files := corpus(4)
	b := writeArchive(t, files, WithDamageProtection(true))
	r := verifyArchive(t, b, files)
	if !r.Protected {
		t.Fatal("not protected")
	}
	for _, off := range []int{5, 700, len(b) / 2} {
		c := bytes.Clone(b)
		c[off] ^= 0x40
		r, err := NewReader(bytes.NewReader(c), int64(len(c)))
		if err == nil && r.Check() == nil {
			t.Fatalf("damage at %d not detected", off)
		}
	}
}

func TestWriterDeterministic(t *testing.T) {
	// The comment is stamped with the current time, which may tick between the two archives.
	t0 := time.Now()
	defer func(f func() time.Time) { now = f }(now)
	now = func() time.Time { return t0 }
	files := corpus(2)
	a := writeArchive(t, files, WithConcurrency(1), WithLevel(Tight))
	b := writeArchive(t, files, WithConcurrency(8), WithLevel(Tight))
	if !bytes.Equal(a, b) {
		t.Fatal("output depends on concurrency")
	}
}

func TestWriterNames(t *testing.T) {
	files := corpus(3)
	b := writeArchive(t, files)
	r := verifyArchive(t, b, files)
	short := map[string]string{}
	for _, f := range r.File {
		short[f.Name] = f.ShortName
	}
	for name, want := range map[string]string{
		"README.TXT": "README.TXT", "readme.md": "README.MD", "src/very long file name.c": "VERYLO~1.C",
		"con.txt": "CON~1.TXT", "src/": "SRC",
	} {
		if short[name] != want {
			t.Errorf("%s: short name %q, want %q", name, short[name], want)
		}
	}
	if err := fstest.TestFS(r, "README.TXT", "src/main.c", "src/日本語.txt", "deep/a/b/c/d/e/f/g/h/i/j/k/file.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestWriterRevisions(t *testing.T) {
	var m memFile
	w := NewWriter(&m)
	for i := range 3 {
		ew, _ := w.Create("dir/file.txt")
		fmt.Fprintf(ew, "revision %d", i)
		ew, _ = w.Create("other.txt")
		fmt.Fprintf(ew, "other %d", i)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(bytes.NewReader(m.b), int64(len(m.b)))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.cdir.Validate(false); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range r.File {
		if f.Name == "dir/file.txt" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			got = append(got, fmt.Sprintf("%d:%s", f.Revision, b))
		}
	}
	if strings.Join(got, ",") != "2:revision 0,1:revision 1,0:revision 2" {
		t.Fatal(got)
	}
}

func TestWriterErrors(t *testing.T) {
	var m memFile
	w := NewWriter(&m)
	for _, n := range []string{"", "/abs", "a/../b", "a//b", "a\\b", "./x"} {
		if _, err := w.Create(n); !errors.Is(err, errBadName) {
			t.Errorf("%q: %v", n, err)
		}
	}
	w.Create("f")
	if _, err := w.Create("f/x"); err == nil {
		t.Error("file used as directory")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Create("late"); !errors.Is(err, errClosed) {
		t.Fatal(err)
	}
}

func TestCopySamples(t *testing.T) {
	for _, p := range samplePaths(t) {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		r, err := NewReader(bytes.NewReader(src), int64(len(src)))
		if err != nil {
			t.Fatal(err)
		}
		var m memFile
		w := NewWriter(&m)
		for _, f := range r.File {
			if err := w.Copy(f); err != nil {
				t.Fatalf("%s: %s: %v", p, f.Name, err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(p, err)
		}
		r2, err := NewReader(bytes.NewReader(m.b), int64(len(m.b)))
		if err != nil {
			t.Fatal(p, err)
		}
		if err := r2.cdir.Validate(false); err != nil {
			t.Fatal(p, err)
		}
		if len(r2.File) != len(r.File) {
			t.Fatalf("%s: %d files, want %d", p, len(r2.File), len(r.File))
		}
		for i, f := range r.File {
			g := r2.File[i]
			if g.Name != f.Name || g.Revision != f.Revision || g.Size != f.Size || !g.Modified.Equal(f.Modified) || g.ShortName != f.ShortName {
				t.Fatalf("%s: entry %d: %+v vs %+v", p, i, g.FileHeader, f.FileHeader)
			}
			a, _ := readAll(f)
			b, err := readAll(g)
			if err != nil || !bytes.Equal(a, b) {
				t.Fatalf("%s: %s: content mismatch: %v", p, f.Name, err)
			}
		}
	}
}

func readAll(f *File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func TestAddFS(t *testing.T) {
	fsys := fstest.MapFS{
		"a.txt":         {Data: []byte("hello"), ModTime: time.Now()},
		"dir/b.txt":     {Data: bytes.Repeat([]byte("b"), 3000), ModTime: time.Now()},
		"dir/sub/c.bin": {Data: make([]byte, 2<<20), ModTime: time.Now()},
		"empty":         {Data: nil, ModTime: time.Now()},
	}
	var m memFile
	w := NewWriter(&m)
	if err := w.AddFS(fsys); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(bytes.NewReader(m.b), int64(len(m.b)))
	if err != nil {
		t.Fatal(err)
	}
	if err := fstest.TestFS(r, "a.txt", "dir/b.txt", "dir/sub/c.bin", "empty"); err != nil {
		t.Fatal(err)
	}
	for name, f := range fsys {
		got, err := fs.ReadFile(r, name)
		if err != nil || !bytes.Equal(got, f.Data) {
			t.Fatal(name, err)
		}
	}
}

func benchFiles(n, size int) []testFile {
	c := corpus(9)
	var files []testFile
	for i := range n {
		src := c[i%3].data
		off := (i * 131) % max(1, len(src)-size)
		files = append(files, testFile{fmt.Sprintf("dir%d/file%d.%s", i%20, i, []string{"c", "h", "txt", "md"}[i%4]), src[off:min(len(src), off+size)]})
	}
	return files
}

func BenchmarkWriter(b *testing.B) {
	large := bytes.Repeat(corpus(10)[2].data, 6000)[:64<<20]
	for _, tc := range []struct {
		name  string
		files []testFile
	}{
		{"small", benchFiles(5000, 4000)},
		{"large", []testFile{{"large.bin", large}}},
	} {
		total := 0
		for _, f := range tc.files {
			total += len(f.data)
		}
		for _, n := range []int{1, 0} {
			b.Run(fmt.Sprintf("%s/threads=%d", tc.name, n), func(b *testing.B) {
				b.SetBytes(int64(total))
				var size int
				for b.Loop() {
					size = len(writeArchive(b, tc.files, WithConcurrency(n)))
				}
				b.ReportMetric(float64(size)/float64(total), "ratio")
			})
		}
	}
}
