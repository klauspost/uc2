// Package charset provides the DOS OEM code pages used for UC2 names.
package charset

// Table maps bytes 0x80..0xFF to runes; bytes below 0x80 are ASCII.
type Table [128]rune

func (t *Table) DecodeByte(b byte) rune {
	if b < 0x80 {
		return rune(b)
	}
	return t[b-0x80]
}

func (t *Table) EncodeRune(r rune) (byte, bool) {
	if r < 0x80 {
		return byte(r), true
	}
	for i, v := range t {
		if v == r {
			return byte(i + 0x80), true
		}
	}
	return '?', false
}
