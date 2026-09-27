package ultra

// decTable is a single-level canonical Huffman lookup table indexed by the next
// `bits` stream bits. Entries hold sym | len<<9; len 0 marks an unused code.
type decTable struct {
	bits uint8
	t    [1 << maxBits]uint16
}

// build fills the table from code lengths. Incomplete codes are allowed (UC2's
// default literal tree is one); oversubscribed codes are rejected.
func (d *decTable) build(lens []byte) error {
	var count [maxBits + 1]int
	maxLen := 0
	for _, l := range lens {
		if l > maxBits {
			return ErrCorrupt
		}
		count[l]++
		maxLen = max(maxLen, int(l))
	}
	left := 1
	for l := 1; l <= maxBits; l++ {
		left = left<<1 - count[l]
		if left < 0 {
			return ErrCorrupt
		}
	}
	if maxLen == 0 {
		d.bits = 1
		d.t[0], d.t[1] = 0, 0
		return nil
	}
	d.bits = uint8(maxLen)
	t := d.t[:1<<maxLen]
	idx := 0
	for l := 1; l <= maxLen; l++ {
		if count[l] == 0 {
			continue
		}
		n := 1 << (maxLen - l)
		for s, sl := range lens {
			if int(sl) != l {
				continue
			}
			e := uint16(s) | uint16(l)<<9
			for i := idx; i < idx+n; i++ {
				t[i] = e
			}
			idx += n
		}
	}
	clear(t[idx:])
	return nil
}
