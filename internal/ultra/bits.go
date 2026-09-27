package ultra

// Bits accumulates a bit stream MSB first into little-endian 16-bit words.
type Bits struct {
	out []byte
	acc uint64
	n   uint // pending bits in the low end of acc, < 32
}

// Put appends the low k bits of v (k <= 32, v < 1<<k).
func (b *Bits) Put(v uint32, k uint) {
	b.acc = b.acc<<k | uint64(v)
	b.n += k
	if b.n >= 32 {
		b.n -= 32
		w := uint32(b.acc >> b.n)
		b.out = append(b.out, byte(w>>16), byte(w>>24), byte(w), byte(w>>8))
	}
}

// Len returns the number of bits written.
func (b *Bits) Len() int { return len(b.out)*8 + int(b.n) }

// Append appends all bits of o.
func (b *Bits) Append(o *Bits) {
	if b.n == 0 {
		b.out = append(b.out, o.out...)
	} else {
		for i := 0; i+4 <= len(o.out); i += 4 {
			w := uint32(o.out[i+1])<<24 | uint32(o.out[i])<<16 | uint32(o.out[i+3])<<8 | uint32(o.out[i+2])
			b.Put(w, 32)
		}
	}
	b.Put(uint32(o.acc)&(1<<o.n-1), o.n)
}

// Finish pads the stream with zero bits to a whole 16-bit word and returns it.
func (b *Bits) Finish() []byte {
	if b.n > 0 {
		pad := (16 - b.n%16) % 16
		w := uint32(b.acc<<pad) & (1<<(b.n+pad) - 1)
		if b.n+pad == 32 {
			b.out = append(b.out, byte(w>>16), byte(w>>24), byte(w), byte(w>>8))
		} else {
			b.out = append(b.out, byte(w), byte(w>>8))
		}
		b.n = 0
	}
	return b.out
}

// Drain returns the complete words written so far and removes them from b.
func (b *Bits) Drain() []byte {
	o := b.out
	b.out = b.out[len(b.out):]
	return o
}

// Reset empties b, keeping its buffer.
func (b *Bits) Reset() { *b = Bits{out: b.out[:0]} }
