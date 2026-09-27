package uc2

import (
	"bytes"
	"io"
	"os"
	"sort"
	"testing"

	"github.com/klauspost/uc2/internal/format"
)

// sparseFile is an io.WriteSeeker/io.ReaderAt that stores only written chunks.
type sparseFile struct {
	chunks map[int64][]byte
	pos    int64
	size   int64
}

func (s *sparseFile) Write(p []byte) (int, error) {
	if s.chunks == nil {
		s.chunks = map[int64][]byte{}
	}
	if c := s.chunks[s.pos]; len(c) >= len(p) {
		copy(c, p)
	} else {
		s.chunks[s.pos] = bytes.Clone(p)
	}
	s.pos += int64(len(p))
	s.size = max(s.size, s.pos)
	return len(p), nil
}

func (s *sparseFile) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		off += s.pos
	case io.SeekEnd:
		off += s.size
	}
	s.pos = off
	return off, nil
}

func (s *sparseFile) ReadAt(p []byte, off int64) (int, error) {
	clear(p)
	keys := make([]int64, 0, len(s.chunks))
	for k := range s.chunks {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		c := s.chunks[k]
		lo, hi := max(k, off), min(k+int64(len(c)), off+int64(len(p)))
		if lo < hi {
			copy(p[lo-off:hi-off], c[lo-k:hi-k])
		}
	}
	if off+int64(len(p)) > s.size {
		return int(max(0, s.size-off)), io.EOF
	}
	return len(p), nil
}

// writeWithGap writes files after skipping gap bytes of zeros.
func writeWithGap(t *testing.T, gap int64, files []testFile) *sparseFile {
	t.Helper()
	s := &sparseFile{}
	w := NewWriter(s)
	if err := w.begin(); err != nil {
		t.Fatal(err)
	}
	w.bw.Flush()
	s.pos += gap
	w.off += gap
	for _, f := range files {
		ew, err := w.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		ew.Write(f.data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExtendedMode(t *testing.T) {
	files := corpus(8)[:5]
	for _, tc := range []struct {
		gap    int64
		needed uint16
		vol    uint32
	}{
		{1 << 20, format.Needed, 1},
		{maxCompat - 1000, format.NeededLarge, 1},
		{5 << 30, format.NeededLarge, 2},
	} {
		s := writeWithGap(t, tc.gap, files)
		r, err := NewReader(s, s.size)
		if err != nil {
			t.Fatal(tc.gap, err)
		}
		if r.xh.Needed != tc.needed || r.xh.Cdir.Vol != tc.vol {
			t.Fatalf("gap %d: needed %d vol %d", tc.gap, r.xh.Needed, r.xh.Cdir.Vol)
		}
		if err := r.Check(); err != nil {
			t.Fatal(err)
		}
		for _, f := range r.File {
			if _, err := readAll(f); err != nil {
				t.Fatal(f.Name, err)
			}
		}
		// Readers must not accept 64-bit locations in a standard archive.
		if tc.vol > 1 {
			var head [format.HeadSize]byte
			s.ReadAt(head[:], 0)
			head[format.FHeadSize+13] = byte(format.Needed)
			s.chunks[0] = head[:]
			if _, err := NewReader(s, s.size); err == nil {
				t.Fatal("volume > 1 accepted in a standard archive")
			}
		}
	}
}

func TestDeepPathExtended(t *testing.T) {
	name := ""
	for range 12 {
		name += "directory/"
	}
	var m memFile
	w := NewWriter(&m)
	ew, _ := w.Create(name + "file.txt")
	ew.Write([]byte("deep"))
	w.Close()
	r, err := NewReader(bytes.NewReader(m.b), int64(len(m.b)))
	if err != nil || r.xh.Needed != format.NeededLarge {
		t.Fatal("deep 8.3 paths must use extended mode", err)
	}
}

func TestHugeFile(t *testing.T) {
	if os.Getenv("UC2_BIGTESTS") == "" {
		t.Skip("set UC2_BIGTESTS=1")
	}
	s := &sparseFile{}
	w := NewWriter(s, WithLevel(Fast))
	ew, _ := w.Create("zeros.bin")
	buf := make([]byte, 1<<20)
	const size = 4<<30 + 12345
	for n := int64(0); n < size; n += int64(len(buf)) {
		ew.Write(buf[:min(int64(len(buf)), size-n)])
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(s, s.size)
	if err != nil {
		t.Fatal(err)
	}
	f := r.File[0]
	if f.Size != size || r.xh.Needed != format.NeededLarge {
		t.Fatal(f.Size, r.xh.Needed)
	}
	rc, _ := f.Open()
	n, err := io.Copy(io.Discard, rc)
	if err != nil || n != size {
		t.Fatal(n, err)
	}
}
