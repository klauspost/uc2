package ultra

// Delta is UC2's multimedia filter state: each byte is predicted by the byte
// Size positions earlier (DELTA.CPP). The zero value must be initialized with NewDelta.
type Delta struct {
	size, ctr int
	prev      [8]byte
}

func NewDelta(size int) Delta { return Delta{size: size} }

// Encode replaces b with byte differences in place.
func (d *Delta) Encode(b []byte) {
	if d.size == 1 {
		p := d.prev[0]
		for i, v := range b {
			b[i] = v - p
			p = v
		}
		d.prev[0] = p
		return
	}
	for i, v := range b {
		b[i] = v - d.prev[d.ctr]
		d.prev[d.ctr] = v
		if d.ctr++; d.ctr == d.size {
			d.ctr = 0
		}
	}
}

// Decode reverses Encode in place.
func (d *Delta) Decode(b []byte) {
	if d.size == 1 {
		p := d.prev[0]
		for i, v := range b {
			p += v
			b[i] = p
		}
		d.prev[0] = p
		return
	}
	for i, v := range b {
		v += d.prev[d.ctr]
		b[i] = v
		d.prev[d.ctr] = v
		if d.ctr++; d.ctr == d.size {
			d.ctr = 0
		}
	}
}
