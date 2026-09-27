package ultra

import (
	"os"
	"testing"

	"github.com/klauspost/uc2/internal/super"
)

func TestStrictOwnOutput(t *testing.T) {
	for name, data := range testInputs() {
		for level := 1; level <= 4; level++ {
			for _, dict := range [][]byte{nil, []byte(super.Data)} {
				var d *Dict
				hist := dict
				if dict != nil {
					d = NewDict(dict)
				}
				c := Compress(data, d, level)
				if err := Strict(c, hist, int64(len(data))); err != nil {
					t.Fatalf("%s level %d: %v", name, level, err)
				}
			}
		}
	}
}

func TestStrictUC2Output(t *testing.T) {
	b, err := os.ReadFile("../../testdata/samples/super.bin")
	if err != nil {
		t.Skip("samples missing")
	}
	if err := Strict(b, zero512, 49152); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"bfa0017a0000", "09b2cfd280de0040"} {
		c := unhex(s)
		n := int64(1)
		if len(c) == 8 {
			n = 22
		}
		if err := Strict(c, zero512, n); err != nil {
			t.Fatal(s, err)
		}
	}
	if err := Strict(unhex("bfa0017a000000000000"), zero512, 1); err == nil {
		t.Fatal("trailing data accepted")
	}
}
