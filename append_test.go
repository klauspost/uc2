package uc2

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
)

func (m *memFile) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(m.b)) {
		return 0, io.EOF
	}
	n := copy(p, m.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m *memFile) Truncate(size int64) error {
	if size < int64(len(m.b)) {
		m.b = m.b[:size]
	} else {
		m.b = append(m.b, make([]byte, size-int64(len(m.b)))...)
	}
	return nil
}

func (m *memFile) Sync() error { return nil }

// faultyFile fails the n-th mutating operation.
type faultyFile struct {
	*memFile
	n int
}

var errInjected = errors.New("injected fault")

func (f *faultyFile) tick() error {
	if f.n--; f.n == 0 {
		return errInjected
	}
	return nil
}

func (f *faultyFile) Write(p []byte) (int, error) {
	if err := f.tick(); err != nil {
		return 0, err
	}
	return f.memFile.Write(p)
}

func (f *faultyFile) Truncate(s int64) error {
	if err := f.tick(); err != nil {
		return err
	}
	return f.memFile.Truncate(s)
}

func (f *faultyFile) Sync() error {
	if err := f.tick(); err != nil {
		return err
	}
	return nil
}

func contents(t testing.TB, b []byte) (map[string]string, error) {
	r, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, f := range r.File {
		d, err := readAll(f)
		if err != nil {
			return nil, err
		}
		m[fmt.Sprintf("%s;%d", f.Name, f.Revision)] = string(d)
	}
	return m, nil
}

func appendFiles(t testing.TB, f appendFile, size int64, opts ...Option) error {
	w, err := newAppendWriter(f, size, opts)
	if err != nil {
		return err
	}
	ew, err := w.Create("src/main.c")
	if err != nil {
		return err
	}
	io.WriteString(ew, "new revision of main.c")
	ew, _ = w.Create("added/new.txt")
	io.WriteString(ew, "a brand new file")
	return w.Close()
}

func TestAppend(t *testing.T) {
	for _, protect := range []bool{false, true} {
		files := corpus(5)[:8]
		orig := writeArchive(t, files, WithDamageProtection(protect))
		m := &memFile{b: bytes.Clone(orig)}
		if err := appendFiles(t, m, int64(len(m.b))); err != nil {
			t.Fatal(err)
		}
		r := verifyArchive(t, m.b, append(files[:2:2], append(files[3:], testFile{"src/main.c", []byte("new revision of main.c")}, testFile{"added/new.txt", []byte("a brand new file")})...))
		if r.Protected != protect {
			t.Fatal("protection changed")
		}
		if !bytes.Equal(m.b[29:len(orig)-13], orig[29:len(orig)-13]) {
			t.Fatal("existing data modified")
		}
		c, _ := contents(t, m.b)
		if c["src/main.c;1"] != string(files[2].data) || c["src/main.c;0"] != "new revision of main.c" {
			t.Fatal("revisions wrong")
		}
	}
}

func TestAppendSamples(t *testing.T) {
	for _, p := range samplePaths(t) {
		b, _ := os.ReadFile(p)
		before, err := contents(t, b)
		if err != nil {
			t.Fatal(err)
		}
		m := &memFile{b: bytes.Clone(b)}
		if err := appendFiles(t, m, int64(len(b))); err != nil {
			t.Fatal(p, err)
		}
		after, err := contents(t, m.b)
		if err != nil {
			t.Fatal(p, err)
		}
		for k, v := range before {
			if after[k] != v && k != "src/main.c;0" {
				t.Fatalf("%s: %s changed", p, k)
			}
		}
		r, _ := NewReader(bytes.NewReader(m.b), int64(len(m.b)))
		if err := r.Check(); err != nil {
			t.Fatal(p, err)
		}
		if err := r.cdir.Validate(false); err != nil {
			t.Fatal(p, err)
		}
	}
}

func TestProtectUnprotect(t *testing.T) {
	orig := writeArchive(t, corpus(6)[:6])
	m := &memFile{b: bytes.Clone(orig)}
	w, err := newAppendWriter(m, int64(len(m.b)), []Option{WithDamageProtection(true)})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(bytes.NewReader(m.b), int64(len(m.b)))
	if err != nil || !r.Protected || r.Check() != nil {
		t.Fatal("protect failed", err)
	}
	w, _ = newAppendWriter(m, int64(len(m.b)), []Option{WithDamageProtection(false)})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(m.b, orig) {
		t.Fatal("protect+unprotect did not restore the archive")
	}
}

func TestAppendFaults(t *testing.T) {
	files := corpus(7)[:6]
	orig := writeArchive(t, files)
	want, _ := contents(t, orig)
	full := &memFile{b: bytes.Clone(orig)}
	appendFiles(t, full, int64(len(orig)))
	wantNew, _ := contents(t, full.b)
	for _, op := range []struct {
		name string
		run  func(f appendFile, size int64) error
		want []map[string]string
	}{
		{"append", func(f appendFile, size int64) error { return appendFiles(t, f, size) }, []map[string]string{want, wantNew}},
		{"protect", func(f appendFile, size int64) error {
			w, err := newAppendWriter(f, size, []Option{WithDamageProtection(true)})
			if err != nil {
				return err
			}
			return w.Close()
		}, []map[string]string{want}},
	} {
		for n := 1; n < 50; n++ {
			m := &memFile{b: bytes.Clone(orig)}
			err := op.run(&faultyFile{memFile: m, n: n}, int64(len(orig)))
			got, rerr := contents(t, m.b)
			if rerr != nil {
				t.Fatalf("%s: fault %d (%v): archive unreadable: %v", op.name, n, err, rerr)
			}
			ok := false
			for _, w := range op.want {
				ok = ok || fmt.Sprint(got) == fmt.Sprint(w)
			}
			if !ok {
				t.Fatalf("%s: fault %d: unexpected contents", op.name, n)
			}
			if err == nil {
				break
			}
		}
	}
}
