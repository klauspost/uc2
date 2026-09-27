// Package dp implements UC2 damage protection (DAMPRO.CPP): XOR parity sectors
// and per-sector checksums appended after the archive, allowing the repair of
// one damaged 512-byte sector per parity class.
package dp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"

	"github.com/klauspost/uc2/internal/format"
)

const sector = 512

var ErrUnrepairable = errors.New("uc2: damage cannot be repaired")

// Geometry returns the number of protected sectors (including the padded last
// one) and parity sectors for an archive of l bytes before protection.
func Geometry(l int64) (secs, drs int64) {
	secs = l/sector + 1
	switch {
	case secs < 200:
		drs = 1
	case secs < 400:
		drs = 2
	case secs < 800:
		drs = 4
	case secs < 1600:
		drs = 8
	default:
		drs = 16
	}
	return secs, drs
}

// AreaSize returns the size of the protection area appended after l bytes.
func AreaSize(l int64) int64 {
	secs, drs := Geometry(l)
	return secs*sector - l + drs*sector + 2*secs + 2
}

// Sum accumulates protection data for an archive written sequentially.
type Sum struct {
	par   [16][sector]byte
	sums  []byte
	cur   [sector]byte
	n     int
	first [sector]byte
	l     int64
}

func (s *Sum) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		c := copy(s.cur[s.n:], p)
		s.n += c
		s.l += int64(c)
		p = p[c:]
		if s.n == sector {
			s.sector()
		}
	}
	return n, nil
}

func (s *Sum) sector() {
	idx := len(s.sums) / 2
	if idx == 0 {
		s.first = s.cur
	}
	xorSector(&s.par[idx%16], &s.cur)
	s.sums = binary.LittleEndian.AppendUint16(s.sums, format.Fletch(s.cur[:]))
	s.n = 0
}

// Area finishes the protection data. head replaces the first len(head) bytes
// written (a header patched after writing). It returns the bytes to append.
func (s *Sum) Area(head []byte) []byte {
	l := s.l
	padLen := int((l/sector+1)*sector - l)
	clear(s.cur[s.n:])
	s.sector()
	secs, drs := Geometry(l)
	fixed := s.first
	copy(fixed[:], head)
	diff := s.first
	xorSector(&diff, &fixed)
	xorSector(&s.par[0], &diff)
	binary.LittleEndian.PutUint16(s.sums, format.Fletch(fixed[:]))

	out := make([]byte, padLen, AreaSize(l))
	for x := range drs {
		var p [sector]byte
		for k := x; k < 16; k += drs {
			xorSector(&p, &s.par[k])
		}
		out = append(out, p[:]...)
	}
	out = append(out, s.sums[:2*secs]...)
	return binary.LittleEndian.AppendUint16(out, format.Fletch(s.sums[:2*secs]))
}

func xorSector(dst, src *[sector]byte) {
	for i := 0; i < sector; i += 8 {
		binary.LittleEndian.PutUint64(dst[i:], binary.LittleEndian.Uint64(dst[i:])^binary.LittleEndian.Uint64(src[i:]))
	}
}

// Result describes the state of a protected archive.
type Result struct {
	TableOK   bool    // the checksum table itself is intact
	Bad       []int64 // damaged sectors
	ParityBad []int64 // damaged parity sectors (data intact)
}

func (r *Result) OK() bool { return r.TableOK && len(r.Bad) == 0 && len(r.ParityBad) == 0 }

// Verify checks the protection of an archive of l bytes (before protection) in ra.
func Verify(ra io.ReaderAt, l int64) (*Result, error) {
	secs, drs := Geometry(l)
	sums, err := readSums(ra, secs, drs)
	res := &Result{TableOK: err == nil}
	if err != nil {
		return res, nil
	}
	var par [16][sector]byte
	var buf [sector]byte
	for i := range secs {
		if _, err := ra.ReadAt(buf[:], i*sector); err != nil {
			return nil, err
		}
		xorSector(&par[i%drs], &buf)
		if format.Fletch(buf[:]) != binary.LittleEndian.Uint16(sums[2*i:]) {
			res.Bad = append(res.Bad, i)
		}
	}
	if len(res.Bad) == 0 {
		for x := range drs {
			if _, err := ra.ReadAt(buf[:], (secs+x)*sector); err != nil {
				return nil, err
			}
			if buf != par[x] {
				res.ParityBad = append(res.ParityBad, x)
			}
		}
	}
	return res, nil
}

func readSums(ra io.ReaderAt, secs, drs int64) ([]byte, error) {
	sums := make([]byte, 2*secs+2)
	if _, err := ra.ReadAt(sums, (secs+drs)*sector); err != nil {
		return nil, err
	}
	if format.Fletch(sums[:2*secs]) != binary.LittleEndian.Uint16(sums[2*secs:]) {
		return nil, errors.New("uc2: damage protection table is damaged")
	}
	return sums[:2*secs], nil
}

// Repair reconstructs the damaged sectors listed in res, at most one per
// parity class. It returns the repaired sector contents by sector index.
func Repair(ra io.ReaderAt, l int64, res *Result) (map[int64][]byte, error) {
	if !res.TableOK {
		return nil, ErrUnrepairable
	}
	secs, drs := Geometry(l)
	sums, err := readSums(ra, secs, drs)
	if err != nil {
		return nil, ErrUnrepairable
	}
	bad := map[int64]int64{} // parity class -> damaged sector
	var par [16][sector]byte
	for _, b := range res.Bad {
		x := b % drs
		if _, dup := bad[x]; dup {
			return nil, ErrUnrepairable
		}
		bad[x] = b
		if _, err := ra.ReadAt(par[x][:], (secs+x)*sector); err != nil {
			return nil, err
		}
	}
	var buf [sector]byte
	for i := range secs {
		if b, ok := bad[i%drs]; ok && b != i {
			if _, err := ra.ReadAt(buf[:], i*sector); err != nil {
				return nil, err
			}
			xorSector(&par[i%drs], &buf)
		}
	}
	fixed := map[int64][]byte{}
	for x, b := range bad {
		if format.Fletch(par[x][:]) != binary.LittleEndian.Uint16(sums[2*b:]) {
			return nil, ErrUnrepairable
		}
		fixed[b] = bytes.Clone(par[x][:])
	}
	return fixed, nil
}
