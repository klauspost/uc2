package dp

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"testing"

	"github.com/klauspost/uc2/internal/format"
)

func lcg(n int) []byte {
	b := make([]byte, n)
	x := uint32(1)
	for i := range b {
		x = x*1103515245 + 12345
		b[i] = byte(x >> 16)
	}
	return b
}

func protect(data []byte) []byte {
	var s Sum
	s.Write(data)
	return append(bytes.Clone(data), s.Area(nil)...)
}

func TestVectors(t *testing.T) {
	for _, tc := range []struct {
		l          int
		secs, drs  int64
		chk        []uint16
		table      uint16
		par0, size string
	}{
		{1000, 2, 1, []uint16{0xA8C8, 0xB479}, 0xB9EB, "5f6dd0b9718c4fc6", ""},
		{1024, 3, 1, []uint16{0xA8C8, 0x1B3E, 0xA55A}, 0xB3F6, "5f6dd0b9718c4fc6", ""},
		{204800, 401, 4, []uint16{0xA8C8, 0x1B3E, 0x72CA}, 0xAB38, "4687852c32dd41d1", ""},
	} {
		data := lcg(tc.l)
		out := protect(data)
		secs, drs := Geometry(int64(tc.l))
		if secs != tc.secs || drs != tc.drs {
			t.Fatalf("%d: geometry %d %d", tc.l, secs, drs)
		}
		if int64(len(out)) != int64(tc.l)+AreaSize(int64(tc.l)) {
			t.Fatalf("%d: size %d", tc.l, len(out))
		}
		sums := out[(secs+drs)*sector:]
		for i, c := range tc.chk {
			if got := binary.LittleEndian.Uint16(sums[2*i:]); got != c {
				t.Errorf("%d: chk[%d] = %04x, want %04x", tc.l, i, got, c)
			}
		}
		if got := binary.LittleEndian.Uint16(sums[2*secs:]); got != tc.table {
			t.Errorf("%d: table %04x, want %04x", tc.l, got, tc.table)
		}
		if got := hex.EncodeToString(out[secs*sector:][:8]); got != tc.par0 {
			t.Errorf("%d: parity %s, want %s", tc.l, got, tc.par0)
		}
		res, err := Verify(bytes.NewReader(out), int64(tc.l))
		if err != nil || !res.OK() {
			t.Fatalf("%d: verify %+v %v", tc.l, res, err)
		}
	}
}

func TestHeaderPatch(t *testing.T) {
	data := lcg(3000)
	var s Sum
	s.Write(data)
	head := []byte("PATCHED HEADER")
	area := s.Area(head)
	copy(data, head)
	if res, err := Verify(bytes.NewReader(append(data, area...)), 3000); err != nil || !res.OK() {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRepair(t *testing.T) {
	data := lcg(900 * sector)
	out := protect(data)
	secs, drs := Geometry(int64(len(data)))
	damaged := bytes.Clone(out)
	for i := range drs {
		damaged[(100+i)*sector+17] ^= 0xFF
	}
	res, _ := Verify(bytes.NewReader(damaged), int64(len(data)))
	if len(res.Bad) != int(drs) {
		t.Fatalf("bad sectors %v", res.Bad)
	}
	fixed, err := Repair(bytes.NewReader(damaged), int64(len(data)), res)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range fixed {
		copy(damaged[i*sector:], b)
	}
	if !bytes.Equal(damaged, out) {
		t.Fatal("repair mismatch")
	}
	// Two bad sectors in one class cannot be repaired.
	damaged[5*sector] ^= 1
	damaged[(5+drs)*sector] ^= 1
	res, _ = Verify(bytes.NewReader(damaged), int64(len(data)))
	if _, err := Repair(bytes.NewReader(damaged), int64(len(data)), res); err == nil {
		t.Fatal("repaired two sectors of one class")
	}
	_ = secs
}

func TestSamples(t *testing.T) {
	for name, l := range map[string]int64{"chaos100.uc2": 4561, "1661651138_WARLORDS.UC2": 316793} {
		b, err := os.ReadFile("../../testdata/samples/" + name)
		if err != nil {
			t.Skip("samples missing")
		}
		fh, _ := format.ParseFHead(b)
		if int64(fh.CompLen)+13 != l || int64(len(b)) != l+AreaSize(l)+13 {
			t.Fatalf("%s: geometry", name)
		}
		res, err := Verify(bytes.NewReader(b), l)
		if err != nil || !res.OK() {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
	}
}
