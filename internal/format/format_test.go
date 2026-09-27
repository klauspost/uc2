package format

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"testing"
	"time"
)

func TestFletcher(t *testing.T) {
	for in, want := range map[string]uint16{
		"": 0xA55A, "a": 0xA53B, "ab": 0xC73B, "abc": 0xC758, "abcd": 0xA358,
		"123456789": 0xAD63, "\x5a\xa5": 0, string(make([]byte, 512)): 0xA55A,
		string(bytes.Repeat([]byte{0xff}, 512)): 0xA55A,
	} {
		if got := Fletch([]byte(in)); got != want {
			t.Errorf("Fletch(%q) = %04x, want %04x", in, got, want)
		}
	}
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i*7 + i>>3)
	}
	want := Fletch(data)
	for step := 1; step < 70; step++ {
		var f Fletcher
		for i := 0; i < len(data); i += step {
			f.Write(data[i:min(i+step, len(data))])
		}
		if f.Sum16() != want {
			t.Fatalf("step %d: %04x != %04x", step, f.Sum16(), want)
		}
	}
}

func TestToKey(t *testing.T) {
	for in, want := range map[string]uint32{
		"X.C": 0x01432000, "X.H": 0x01482000, "X.CPP": 0x01435050, "X.001": 0x01232323, "MAKEFILE": 0x024D414B,
	} {
		base, ext, _ := bytes.Cut([]byte(in), []byte("."))
		n, _ := MakeName(base, ext)
		if got := ToKey(n); got != want {
			t.Errorf("ToKey(%s) = %08x, want %08x", in, got, want)
		}
	}
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// The 60-byte empty archive derived from the r2 source.
var emptyArchive = mustHex("55433" + "21a22000000f6c3b20100" +
	"010000001d0000005ea500ca00c80000" +
	"00000000030001000000" + "09b2cfd280de0040" +
	"554332" + "1a22000000f6c3b20100")

func TestEmptyArchiveHeaders(t *testing.T) {
	if len(emptyArchive) != 60 {
		t.Fatal(len(emptyArchive))
	}
	fh, err := ParseFHead(emptyArchive)
	if err != nil || fh.CompLen != 34 || fh.Protected {
		t.Fatal(fh, err)
	}
	if !bytes.Equal(fh.Append(nil), emptyArchive[:13]) || !bytes.Equal(emptyArchive[47:], emptyArchive[:13]) {
		t.Fatal("fhead round trip")
	}
	xh, err := ParseXHead(emptyArchive[13:])
	if err != nil {
		t.Fatal(err)
	}
	want := XHead{Cdir: Loc{1, 29}, Fletch: 0xA55E, MadeBy: 202, Needed: 200}
	if xh != want || !bytes.Equal(xh.Append(nil), emptyArchive[13:29]) {
		t.Fatalf("%+v", xh)
	}
	if c := ParseCompress(emptyArchive[29:]); c != (Compress{0, 3, NoMaster}) || !bytes.Equal(c.Append(nil), emptyArchive[29:39]) {
		t.Fatalf("%+v", c)
	}
	raw := append([]byte{BoEOL}, make([]byte, 21)...)
	if Fletch(raw) != 0xA55E {
		t.Fatal("empty cdir checksum")
	}
	c, err := ParseCDIR(raw, math.MaxInt)
	if err != nil || len(c.Entries) != 0 || !bytes.Equal(c.Append(nil), raw) {
		t.Fatal(c, err)
	}
	if err := c.Validate(false); err != nil {
		t.Fatal(err)
	}
	// Truncated tails read as zeros.
	if c, err := ParseCDIR([]byte{BoEOL, 1}, math.MaxInt); err != nil || c.Tail.Beta != 1 {
		t.Fatal(c, err)
	}
}

func TestParseFHeadBad(t *testing.T) {
	b := bytes.Clone(emptyArchive[:13])
	b[8]++
	if _, err := ParseFHead(b); !errors.Is(err, ErrFormat) {
		t.Fatal(err)
	}
	if _, err := ParseFHead([]byte("UE2S\x1a")); !errors.Is(err, ErrFormat) {
		t.Fatal(err)
	}
}

func name(s string) Name {
	base, ext, _ := bytes.Cut([]byte(s), []byte("."))
	n, ok := MakeName(base, ext)
	if !ok {
		panic(s)
	}
	return n
}

func sampleCDIR() *CDIR {
	return &CDIR{
		Entries: []Entry{
			{Type: BoDir, Meta: Meta{Name: name("SUB"), Attr: 0x10}, Index: 1, Tags: []Tag{{"AIP:Win95 LongN", []byte("Sub dir\x00")}}},
			{Type: BoFile, Meta: Meta{Name: name("A.TXT")}, Size: 5, Fletch: 7, Comp: Compress{10, 3, 2}, Loc: Loc{1, 29}},
			{Type: BoFile, Meta: Meta{Name: name("A.TXT")}, Size: 6, Fletch: 8, Comp: Compress{12, 3, 2}, Loc: Loc{1, 39}},
			{Type: BoFile, Meta: Meta{Name: name("B.TXT")}, Size: 6, Fletch: 8, Comp: Compress{12, 3, 2}, Loc: Loc{1, 51},
				Tags: []Tag{{"X", nil}, {"UC2X:UTF8Name", []byte("b.txt")}}},
			{Type: BoFile, Meta: Meta{Parent: 1, Name: name("C")}, Comp: Compress{2, 4, 2}, Loc: Loc{1, 63}},
		},
		Masters: []Master{{Index: 2, Key: 0x01545854, Len: 512, Comp: Compress{20, 3, SuperMaster}, Loc: Loc{1, 65}}},
		Tail:    Tail{Label: [11]byte{'L', 'A', 'B', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' '}},
	}
}

func TestCDIRRoundTrip(t *testing.T) {
	c := sampleCDIR()
	if err := c.Validate(false); err != nil {
		t.Fatal(err)
	}
	raw := c.Append(nil)
	c2, err := ParseCDIR(raw, math.MaxInt)
	if err != nil {
		t.Fatal(err)
	}
	if raw2 := c2.Append(nil); !bytes.Equal(raw, raw2) {
		t.Fatal("round trip mismatch")
	}
	if got := c2.Entries[3].Tags[1]; got.Name != "UC2X:UTF8Name" || string(got.Data) != "b.txt" {
		t.Fatal(got)
	}
	for i := range len(raw) - 22 {
		if _, err := ParseCDIR(raw[:i], math.MaxInt); err == nil {
			t.Fatalf("truncated at %d parsed", i)
		}
	}
}

func TestValidate(t *testing.T) {
	for name, mod := range map[string]func(c *CDIR){
		"prefix1":     func(c *CDIR) { c.Entries[1].Comp.Prefix = NoMaster },
		"no master":   func(c *CDIR) { c.Entries[1].Comp.Prefix = 3 },
		"vol":         func(c *CDIR) { c.Entries[1].Loc.Vol = 2 },
		"parent":      func(c *CDIR) { c.Entries[4].Meta.Parent = 7 },
		"dup dir":     func(c *CDIR) { c.Entries[0].Meta.Name = name("B.TXT") },
		"split revs":  func(c *CDIR) { c.Entries[3].Meta.Name = name("A.TXT"); c.Entries[2].Meta.Name = name("Z") },
		"master len":  func(c *CDIR) { c.Masters[0].Len = 1000 },
		"master pfx":  func(c *CDIR) { c.Masters[0].Comp.Prefix = 2 },
		"method":      func(c *CDIR) { c.Entries[1].Comp.Method = 38 },
		"turbo":       func(c *CDIR) { c.Entries[1].Comp.Method = 80 },
		"tag name":    func(c *CDIR) { c.Entries[3].Tags[0].Name = "" },
		"dir index 0": func(c *CDIR) { c.Entries[0].Index = 0 },
		"split run": func(c *CDIR) {
			c.Entries = append(c.Entries, Entry{Type: BoFile, Meta: Meta{Name: name("D")}, Comp: Compress{2, 3, 2}, Loc: Loc{1, 1}})
		},
	} {
		c := sampleCDIR()
		mod(c)
		if err := c.Validate(false); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	c := sampleCDIR()
	c.Entries[1].Loc.Vol = 2
	if err := c.Validate(true); err != nil {
		t.Fatal(err)
	}
}

func TestDOSTime(t *testing.T) {
	tm := time.Date(1993, 3, 17, 13, 30, 59, 0, time.Local)
	d, h := DOSTime(tm)
	if got := FromDOS(d, h, time.Local); !got.Equal(tm.Add(-time.Second)) {
		t.Fatal(got)
	}
	d, h = DOSTime(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC))
	if d != 1<<5|1 || h != 0 {
		t.Fatal(d, h)
	}
	d, h = DOSTime(time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC))
	if got := FromDOS(d, h, time.UTC); !got.Equal(maxDOS) {
		t.Fatal(got)
	}
}

func TestLoc(t *testing.T) {
	for _, off := range []int64{0, 29, 1<<32 - 1, 1 << 32, 5<<32 + 7} {
		if l := LocOf(off); l.Abs() != off || (off < 1<<32) != (l.Vol == 1) {
			t.Fatal(off, l)
		}
	}
}
