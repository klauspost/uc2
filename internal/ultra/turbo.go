package ultra

import (
	"bufio"
	"encoding/binary"
	"io"
)

// TurboReader decodes UC2's method 80 (COMP_TT.CPP): an order-2 byte predictor
// seeded from the dictionary, stored as blocks of control bytes and literals.
type TurboReader struct {
	src    *bufio.Reader
	table  [32768]byte
	p1, p2 byte
	in     []byte
	out    []byte
	pos    int
	left   int64
	err    error
}

func NewTurboReader(src io.Reader, dict []byte, size int64) *TurboReader {
	t := &TurboReader{src: bufio.NewReader(src), left: size}
	for i := 0; i+10 < len(dict); i++ {
		t.table[int(dict[i])<<7^int(dict[i+1])] = dict[i+2]
	}
	return t
}

func (t *TurboReader) Read(p []byte) (int, error) {
	for t.pos == len(t.out) {
		if t.err != nil {
			return 0, t.err
		}
		if t.left <= 0 {
			return 0, io.EOF
		}
		t.err = t.block()
	}
	n := copy(p, t.out[t.pos:])
	t.pos += n
	return n, nil
}

func (t *TurboReader) block() error {
	var hdr [2]byte
	if _, err := io.ReadFull(t.src, hdr[:]); err != nil {
		return io.ErrUnexpectedEOF
	}
	n := int(binary.LittleEndian.Uint16(hdr[:]))
	if n == 0 {
		return io.ErrUnexpectedEOF
	}
	t.in = append(t.in[:0], make([]byte, n)...)
	if _, err := io.ReadFull(t.src, t.in); err != nil {
		return io.ErrUnexpectedEOF
	}
	in, out := t.in, t.out[:0]
	for p := 0; p < len(in); {
		ctl := in[p]
		p++
		for i := 0; i < 8 && (p < len(in) || ctl != 0); i++ {
			h := int(t.p2)<<7 ^ int(t.p1)
			c := t.table[h]
			if ctl&0x80 == 0 {
				if p >= len(in) {
					return ErrCorrupt
				}
				c = in[p]
				p++
				t.table[h] = c
			}
			out = append(out, c)
			t.p2, t.p1 = t.p1, c
			ctl <<= 1
		}
	}
	if int64(len(out)) > t.left {
		out = out[:t.left]
	}
	t.left -= int64(len(out))
	t.out, t.pos = out, 0
	return nil
}
