package ultra

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/klauspost/uc2/internal/super"
)

func testInputs() map[string][]byte {
	rng := rand.New(rand.NewPCG(1, 2))
	random := make([]byte, 300000)
	for i := range random {
		random[i] = byte(rng.Uint32())
	}
	var text bytes.Buffer
	words := []string{"the", "quick", "brown", "fox", "jumps", "over", "lazy", "dog", "UltraCompressor", "archive", "\r\n"}
	for text.Len() < 400000 {
		text.WriteString(words[rng.IntN(len(words))])
		text.WriteByte(' ')
	}
	wav := make([]byte, 200000)
	for i := 0; i < len(wav); i += 2 {
		binary.LittleEndian.PutUint16(wav[i:], uint16(int16(8000*math.Sin(float64(i)/40))+int16(rng.IntN(64))))
	}
	return map[string][]byte{
		"empty": nil, "one": {7}, "two": {7, 7}, "zeros": make([]byte, 200000),
		"random": random, "text": text.Bytes(), "super": []byte(super.Data), "wav": wav,
		"long": bytes.Repeat([]byte("abcdefgh"), 20000),
	}
}

func roundTrip(t *testing.T, name string, data []byte, dict []byte, level int) []byte {
	t.Helper()
	var d *Dict
	hist := zero512
	if dict != nil {
		d, hist = NewDict(dict), dict
	}
	c := Compress(data, d, level)
	if len(c)%2 != 0 {
		t.Fatalf("%s: odd stream length", name)
	}
	got, err := io.ReadAll(NewReader(bytes.NewReader(c), hist, int64(len(data))))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("%s level %d: round trip failed: %v (%d vs %d bytes)", name, level, err, len(got), len(data))
	}
	got, err = io.ReadAll(NewReader(bytes.NewReader(c), hist, Unlimited))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("%s level %d: unsized round trip failed: %v", name, level, err)
	}
	return c
}

func TestGoldenEncode(t *testing.T) {
	if got := Compress([]byte("A"), NewDict(zero512), 3); !bytes.Equal(got, unhex("bfa0017a0000")) {
		t.Fatalf("A: %x", got)
	}
	if got := Compress(nil, nil, 3); !bytes.Equal(got, unhex("0000")) {
		t.Fatalf("empty: %x", got)
	}
}

func TestRoundTrip(t *testing.T) {
	for name, data := range testInputs() {
		for level := 1; level <= 5; level++ {
			if level == 5 && len(data) > 250000 && testing.Short() {
				continue
			}
			c := roundTrip(t, name, data, nil, level)
			roundTrip(t, name+"/super", data, []byte(super.Data), level)
			t.Logf("%s level %d: %d -> %d", name, level, len(data), len(c))
		}
	}
}

func TestSeed(t *testing.T) {
	master := append([]byte("some master content "), []byte(super.Data[1000:40000])...)
	master = append(master, make([]byte, 512-len(master)%512)...)
	for _, tc := range []struct{ off, n int }{{0, 20}, {20, 39000}, {100, 3}, {5000, 32761}, {5000, 32762}, {5000, 32763}} {
		data := append(bytes.Clone(master[tc.off:tc.off+tc.n]), "tail data tail data"...)
		var out Output
		NewEncoder().Encode(&Input{Dict: NewDict(master), Data: data, SeedOff: tc.off, SeedLen: tc.n, Level: 3, First: true, Final: true}, &out)
		c := End(&out.Bits)
		got, err := io.ReadAll(NewReader(bytes.NewReader(c), master, int64(len(data))))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("seed %v: %v", tc, err)
		}
	}
}

func TestFragments(t *testing.T) {
	data := testInputs()["text"]
	data = append(data, testInputs()["wav"]...)
	for _, frag := range []int{1000, 65536, 100003} {
		var s Bits
		prev := BaseLens()
		enc := NewEncoder()
		for off := 0; off < len(data); off += frag {
			end := min(off+frag, len(data))
			var out Output
			enc.Encode(&Input{Hist: data[:off], Data: data[off:end], Level: 3, First: off == 0, Final: end == len(data)}, &out)
			Join(&s, &prev, &out)
		}
		c := End(&s)
		got, err := io.ReadAll(NewReader(bytes.NewReader(c), nil, int64(len(data))))
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("frag %d: %v", frag, err)
		}
	}
}

func TestAnalyze(t *testing.T) {
	in := testInputs()
	if d := Analyze(in["wav"], 4); d == 0 {
		t.Error("wav not detected")
	}
	if d := Analyze(in["text"], 4); d != 0 {
		t.Error("text detected as multimedia", d)
	}
	if d := Analyze(in["wav"], 3); d != 0 {
		t.Error("analyze at level 3")
	}
}

func TestHuffLengths(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for iter := range 2000 {
		n := []int{nLD, nL, nPre}[iter%3]
		maxLen := []int{13, 13, 7}[iter%3]
		freq := make([]uint32, n)
		for i := range freq {
			if rng.IntN(3) > 0 {
				freq[i] = uint32(rng.ExpFloat64() * float64(1+rng.IntN(1<<uint(rng.IntN(16)))))
			}
		}
		lens := make([]byte, n)
		huffLengths(freq, maxLen, lens)
		kraft := 0
		for s, l := range lens {
			if int(l) > maxLen || (freq[s] > 0 && l == 0) {
				t.Fatalf("bad length %d for freq %d", l, freq[s])
			}
			if l > 0 {
				kraft += 1 << (maxLen - int(l))
			}
		}
		if kraft != 1<<maxLen {
			t.Fatalf("incomplete code: %d", kraft)
		}
	}
}

func BenchmarkEncode(b *testing.B) {
	for _, name := range []string{"text", "random", "wav"} {
		data := testInputs()[name]
		for level := 2; level <= 5; level++ {
			b.Run(name+"/"+string(rune('0'+level)), func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				var n int
				for b.Loop() {
					n = len(Compress(data, nil, level))
				}
				b.ReportMetric(float64(n)/float64(len(data)), "ratio")
			})
		}
	}
}

func BenchmarkDecode(b *testing.B) {
	for _, name := range []string{"text", "random", "wav"} {
		data := testInputs()[name]
		c := Compress(data, nil, 3)
		r := NewReader(nil, nil, 0)
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				r.Reset(bytes.NewReader(c), nil, int64(len(data)))
				r.WriteTo(io.Discard)
			}
		})
	}
}

func TestIncompressibleOverhead(t *testing.T) {
	data := testInputs()["random"]
	if n := len(Compress(data, nil, 3)); n > len(data)+len(data)/1000 {
		t.Fatalf("%d bytes for %d random bytes", n, len(data))
	}
}
