package format

import "bytes"

// Name is an FCB-style 8.3 name: 8 name bytes and 3 extension bytes, space padded, no dot.
type Name [11]byte

// Base and Ext return the name parts without trailing padding.
func (n Name) Base() []byte { return bytes.TrimRight(n[:8], " ") }
func (n Name) Ext() []byte  { return bytes.TrimRight(n[8:], " ") }

// Bytes returns the display form "NAME.EXT" (or "NAME"), as UC2's Rep2Name does.
func (n Name) Bytes() []byte {
	b := append([]byte{}, n.Base()...)
	if e := n.Ext(); len(e) > 0 {
		b = append(append(b, '.'), e...)
	}
	return b
}

func (n Name) String() string { return string(n.Bytes()) }

// MakeName builds a Name from base and extension parts, which must be at most 8 and 3 bytes.
func MakeName(base, ext []byte) (n Name, ok bool) {
	if len(base) > 8 || len(ext) > 3 {
		return n, false
	}
	for i := range n {
		n[i] = ' '
	}
	copy(n[:8], base)
	copy(n[8:], ext)
	return n, true
}

// ToKey is UC2's master grouping key (NEUROMAN.CPP ToKey, file type bundling).
// Borland chars are signed, so bytes >= 0x80 are sign extended.
func ToKey(n Name) uint32 {
	sx := func(b byte) uint32 {
		if b >= '0' && b <= '9' {
			b = '#'
		}
		return uint32(int32(int8(b)))
	}
	base, ext := n.Base(), n.Ext()
	if len(ext) > 0 {
		// ".C" is padded to ".C " so the third extension byte stays 0.
		var e [3]byte
		copy(e[:], ext)
		if len(ext) == 1 {
			e[1] = ' '
		}
		return 1<<24 + sx(e[0])<<16 + sx(e[1])<<8 + sx(e[2])
	}
	b := [3]byte{' ', ' ', ' '}
	copy(b[:], base)
	return 2<<24 + sx(b[0])<<16 + sx(b[1])<<8 + sx(b[2])
}
