package ultra

import "encoding/binary"

const hashBits = 16

func hash3(b []byte, i int) uint32 {
	return (binary.LittleEndian.Uint32(b[i:]) << 8) * 0x9E3779B1 >> (32 - hashBits)
}

// Dict is an immutable dictionary ("master") with a prebuilt hash index,
// safe for concurrent use by many encoders.
type Dict struct {
	data []byte
	head []uint16 // position+1 of the newest occurrence per hash; 0 = none
	prev []uint16 // distance to the previous occurrence; 0 = none
}

// NewDict indexes a copy of b. Only the last MaxDist bytes are kept.
func NewDict(b []byte) *Dict {
	b = b[max(0, len(b)-MaxDist):]
	buf := make([]byte, len(b)+8)
	copy(buf, b)
	d := &Dict{data: buf[:len(b)], head: make([]uint16, 1<<hashBits), prev: make([]uint16, len(b))}
	for i := 0; i+3 <= len(b); i++ {
		h := hash3(buf, i)
		if p := d.head[h]; p != 0 {
			d.prev[i] = uint16(i - int(p-1))
		}
		d.head[h] = uint16(i + 1)
	}
	return d
}

// Bytes returns the dictionary content.
func (d *Dict) Bytes() []byte { return d.data }
