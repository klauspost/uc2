package charset

import "testing"

func TestRoundTrip(t *testing.T) {
	for _, tb := range []*Table{CP437, CP850} {
		for i := range 256 {
			r := tb.DecodeByte(byte(i))
			if b, ok := tb.EncodeRune(r); !ok || b != byte(i) {
				t.Fatalf("%d: %q -> %d %v", i, r, b, ok)
			}
		}
	}
	if CP437.DecodeByte(0x81) != 'ü' || CP850.DecodeByte(0xD5) != 'ı' {
		t.Fatal("table")
	}
	if _, ok := CP437.EncodeRune('ı'); ok {
		t.Fatal("unexpected mapping")
	}
}
