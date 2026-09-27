package ultra

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/klauspost/uc2/internal/super"
)

func FuzzDecode(f *testing.F) {
	for _, s := range []string{"0000", "bfa0017a0000", "09b2cfd280de0040", "07b29fb500bd0080", "eabed838ec01f7eb10a00000"} {
		f.Add(unhex(s), uint8(0), uint32(1000))
	}
	if b, err := os.ReadFile("../../testdata/samples/super.bin"); err == nil {
		f.Add(b[:2000], uint8(1), uint32(49152))
	}
	f.Fuzz(func(t *testing.T, src []byte, dictSel uint8, size uint32) {
		dict := zero512
		switch dictSel % 3 {
		case 1:
			dict = []byte(super.Data)
		case 2:
			dict = nil
		}
		n := int64(size % (1 << 20))
		out, _ := io.ReadAll(NewReader(bytes.NewReader(src), dict, n))
		if int64(len(out)) > n {
			t.Fatalf("produced %d > %d", len(out), n)
		}
		if tr, err := io.ReadAll(NewTurboReader(bytes.NewReader(src), dict, n)); err == nil && int64(len(tr)) > n {
			t.Fatal("turbo overrun")
		}
	})
}

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("hello hello hello world"), uint8(3), false)
	f.Add(bytes.Repeat([]byte{0, 1, 2}, 1000), uint8(5), true)
	f.Fuzz(func(t *testing.T, data []byte, level uint8, useSuper bool) {
		var d *Dict
		hist := zero512
		if useSuper {
			d, hist = NewDict([]byte(super.Data)), []byte(super.Data)
		}
		lv := int(level%5) + 1
		c := Compress(data, d, lv)
		if err := Strict(c, hist, int64(len(data))); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(NewReader(bytes.NewReader(c), hist, int64(len(data))))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("round trip", err)
		}
	})
}
