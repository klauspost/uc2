package uc2

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"testing"
)

func FuzzReader(f *testing.F) {
	m, _ := os.ReadDir("testdata/samples")
	for _, e := range m {
		if b, err := os.ReadFile("testdata/samples/" + e.Name()); err == nil && len(b) < 20000 {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return
		}
		r.Check()
		r.Comment()
		for _, f := range r.File {
			if rc, err := f.Open(); err == nil {
				io.Copy(io.Discard, io.LimitReader(rc, 1<<20))
				rc.Close()
			}
		}
		fs.WalkDir(r, ".", func(string, fs.DirEntry, error) error { return nil })
	})
}
