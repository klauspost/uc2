package uc2

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func samplePaths(t testing.TB) []string {
	t.Helper()
	m, _ := filepath.Glob("testdata/samples/*.[uU][cC]2")
	if len(m) == 0 {
		t.Skip("samples missing: run go run testdata/fetch.go")
	}
	return m
}

func TestSamples(t *testing.T) {
	for _, p := range samplePaths(t) {
		t.Run(filepath.Base(p), func(t *testing.T) {
			rc, err := OpenReader(p)
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			if err := rc.Check(); err != nil {
				t.Error("check:", err)
			}
			var files, bytes int64
			for _, f := range rc.File {
				r, err := f.Open()
				if err != nil {
					t.Fatal(f.Name, err)
				}
				n, err := io.Copy(io.Discard, r)
				r.Close()
				if err != nil {
					t.Fatalf("%s: %v", f.Name, err)
				}
				if n != f.Size {
					t.Fatalf("%s: size %d != %d", f.Name, n, f.Size)
				}
				if !strings.HasSuffix(f.Name, "/") {
					files++
					bytes += n
				}
			}
			t.Logf("made by %d, %d files, %d bytes, %d masters, protected %v", rc.MadeBy, files, bytes, len(rc.masters), rc.Protected)
			var names []string
			for _, f := range rc.File {
				if f.Revision == 0 && !strings.HasPrefix(f.ShortName, "U$~") {
					names = append(names, strings.TrimSuffix(f.Name, "/"))
				}
			}
			if err := fstest.TestFS(rc, names...); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestEncrypted(t *testing.T) {
	b, err := os.ReadFile("testdata/samples/unarc-license.ue2")
	if err != nil {
		t.Skip("samples missing")
	}
	if _, err := NewReader(strings.NewReader(string(b)), int64(len(b))); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestSampleCorruption(t *testing.T) {
	paths := samplePaths(t)
	b, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	for i := 29; i < len(b); i += len(b)/300 + 1 {
		c := []byte(string(b))
		c[i] ^= 0x55
		r, err := NewReader(strings.NewReader(string(c)), int64(len(c)))
		if err != nil {
			continue
		}
		for _, f := range r.File {
			if rc, err := f.Open(); err == nil {
				io.Copy(io.Discard, rc)
				rc.Close()
			}
		}
		fs.WalkDir(r, ".", func(string, fs.DirEntry, error) error { return nil })
	}
}
