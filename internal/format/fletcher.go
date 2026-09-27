package format

import "encoding/binary"

// Fletcher is UC2's "Fletcher" checksum, which is actually
// 0xA55A XOR the XOR of all little-endian 16-bit words; an odd final byte is a low byte.
// The result does not depend on how the data is split into writes.
type Fletcher struct {
	x   uint64
	odd bool
}

func (f *Fletcher) Write(p []byte) (int, error) {
	n := len(p)
	if f.odd && len(p) > 0 {
		f.x ^= uint64(p[0]) << 8
		p = p[1:]
		f.odd = false
	}
	x := f.x
	for len(p) >= 32 {
		x ^= binary.LittleEndian.Uint64(p) ^ binary.LittleEndian.Uint64(p[8:]) ^
			binary.LittleEndian.Uint64(p[16:]) ^ binary.LittleEndian.Uint64(p[24:])
		p = p[32:]
	}
	for len(p) >= 8 {
		x ^= binary.LittleEndian.Uint64(p)
		p = p[8:]
	}
	for len(p) >= 2 {
		x ^= uint64(binary.LittleEndian.Uint16(p))
		p = p[2:]
	}
	if len(p) == 1 {
		x ^= uint64(p[0])
		f.odd = true
	}
	f.x = x
	return n, nil
}

func (f *Fletcher) Sum16() uint16 {
	x := f.x
	return uint16(x^x>>16^x>>32^x>>48) ^ 0xA55A
}

// Fletch returns the checksum of p.
func Fletch(p []byte) uint16 {
	var f Fletcher
	f.Write(p)
	return f.Sum16()
}
