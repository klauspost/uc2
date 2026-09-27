package ultra

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/klauspost/uc2/internal/super"
)

func unhex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

var zero512 = make([]byte, 512)

func decodeAll(t *testing.T, src, dict []byte, size int64) []byte {
	t.Helper()
	out, err := io.ReadAll(NewReader(bytes.NewReader(src), dict, size))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGoldenDecode(t *testing.T) {
	cdir := append([]byte{4}, make([]byte, 21)...)
	for _, tc := range []struct {
		name, stream string
		dict         []byte
		size         int64
		want         []byte
	}{
		{"empty", "0000", zero512, Unlimited, nil},
		{"empty sized", "0000", zero512, 0, nil},
		{"A", "bfa0017a0000", zero512, Unlimited, []byte("A")},
		{"A sized", "bfa0017a0000", zero512, 1, []byte("A")},
		{"cdir d52", "09b2cfd280de0040", zero512, Unlimited, cdir},
		{"cdir d22", "07b29fb500bd0080", zero512, Unlimited, cdir},
		{"zeros vs super", "eabed838ec01f7eb10a00000", []byte(super.Data), 512, make([]byte, 512)},
	} {
		got := decodeAll(t, unhex(tc.stream), tc.dict, tc.size)
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%s: got %x want %x", tc.name, got, tc.want)
		}
	}
}

func TestSuperBin(t *testing.T) {
	b, err := os.ReadFile("../../testdata/samples/super.bin")
	if err != nil {
		t.Skip("run go run testdata/fetch.go")
	}
	if got := decodeAll(t, b, zero512, Unlimited); string(got) != super.Data {
		t.Fatalf("supermaster mismatch, got %d bytes", len(got))
	}
	if got := decodeAll(t, b, zero512, 49152); string(got) != super.Data {
		t.Fatal("sized supermaster mismatch")
	}
	// Truncated streams must fail.
	if _, err := io.ReadAll(NewReader(bytes.NewReader(b[:len(b)/2]), zero512, 49152)); err == nil {
		t.Fatal("truncated stream decoded")
	}
}

func TestDeltaTables(t *testing.T) {
	want := [14][14]byte{
		{0, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 0},
		{2, 1, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 0},
		{3, 2, 4, 1, 5, 6, 7, 8, 9, 10, 11, 12, 13, 0},
		{4, 3, 5, 2, 6, 1, 7, 8, 9, 10, 11, 12, 13, 0},
		{5, 4, 6, 3, 7, 2, 8, 1, 9, 10, 11, 12, 13, 0},
		{6, 5, 7, 4, 8, 3, 9, 2, 10, 1, 11, 12, 13, 0},
		{7, 6, 8, 5, 9, 4, 10, 3, 11, 2, 12, 1, 13, 0},
		{8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 0, 1},
		{9, 8, 10, 7, 11, 6, 12, 5, 13, 4, 0, 3, 2, 1},
		{10, 9, 11, 8, 12, 7, 13, 6, 0, 5, 4, 3, 2, 1},
		{11, 10, 12, 9, 13, 8, 0, 7, 6, 5, 4, 3, 2, 1},
		{12, 11, 13, 10, 0, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		{13, 12, 0, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
	}
	if deltaDec != want {
		t.Fatalf("vval mismatch:\n%v", deltaDec)
	}
	for p := range 14 {
		for n := range 14 {
			if deltaDec[p][deltaEnc[p][n]] != byte(n) {
				t.Fatalf("table/vval not inverse at %d,%d", p, n)
			}
		}
	}
}

func TestBaseTree(t *testing.T) {
	kraft := 0
	for _, l := range baseLens[:nLD] {
		kraft += 1024 >> l
	}
	if kraft != 1016 {
		t.Fatal("default LD kraft", kraft)
	}
	var d decTable
	if err := d.build(baseLens[:nLD]); err != nil {
		t.Fatal(err)
	}
	// 'A' has the 8-bit code 10000010; LD315 is 1111110111.
	if e := d.t[0b10000010<<2]; e&511 != 'A' || e>>9 != 8 {
		t.Fatalf("A entry %x", e)
	}
	if e := d.t[0b1111110111]; e&511 != 315 || e>>9 != 10 {
		t.Fatalf("315 entry %x", e)
	}
	if d.t[0b1111111000] != 0 {
		t.Fatal("unused code filled")
	}
	bad := []byte{1, 1, 1}
	if err := d.build(bad); err == nil {
		t.Fatal("oversubscribed accepted")
	}
}

func TestDelta(t *testing.T) {
	in := []byte("the quick brown fox jumps over the lazy dog 0123456789")
	for size := 1; size <= 8; size++ {
		b := bytes.Clone(in)
		e, d := NewDelta(size), NewDelta(size)
		e.Encode(b[:7])
		e.Encode(b[7:])
		d.Decode(b[:13])
		d.Decode(b[13:])
		if !bytes.Equal(b, in) {
			t.Fatal(size)
		}
	}
}

type stuckReader struct{}

func (stuckReader) Read([]byte) (int, error) { return 0, nil }

func TestNoProgress(t *testing.T) {
	if _, err := io.ReadAll(NewReader(stuckReader{}, zero512, Unlimited)); !errors.Is(err, io.ErrNoProgress) {
		t.Fatal(err)
	}
}
