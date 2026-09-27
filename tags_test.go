package uc2

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/klauspost/uc2/internal/format"
	"github.com/klauspost/uc2/internal/tags"
)

func tagList(t []format.Tag) string {
	var s []string
	for _, x := range t {
		s = append(s, fmt.Sprintf("%s=%q", x.Name, x.Data))
	}
	return strings.Join(s, " ")
}

// replaced opens archive b for appending, replaces the tags of the entries
// named in set and returns the archive after Close.
func replaced(t *testing.T, b []byte, set map[string][]format.Tag) []byte {
	t.Helper()
	m := &memFile{b: bytes.Clone(b)}
	w, err := newAppendWriter(m, int64(len(m.b)), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range tags.Source(w).(*Reader).File {
		if l, ok := set[f.Name]; ok {
			if err := tags.Replace(w, f, l); err != nil {
				t.Fatal(f.Name, err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return m.b
}

func recordTags(t *testing.T, b []byte) map[string]string {
	t.Helper()
	r, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Check(); err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, f := range r.File {
		m[f.Name] = tagList(tags.Record(f).Tags)
	}
	return m
}

func TestTagsBridge(t *testing.T) {
	b := writeArchive(t, []testFile{{"Größe.txt", []byte("umlaut")}, {"DIR/A.TXT", []byte("a")}, {"long directory/X", []byte("x")}})
	r, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if l := tags.CDIR(r).Tail.Label; string(l[:]) != "GOLABEL    " {
		t.Errorf("label %q", l)
	}
	if tags.Source(NewWriter(&memFile{})) != nil {
		t.Error("Source of a new archive's writer")
	}
	before := recordTags(t, b)
	if !strings.Contains(before["Größe.txt"], tagUTF8Name) || !strings.Contains(before["long directory/"], tagLongName) {
		t.Fatalf("name tags missing: %v", before)
	}

	a, bb := format.Tag{Name: "TEST:A", Data: []byte("a")}, format.Tag{Name: "TEST:B"}
	fake := []format.Tag{{Name: tagLongName, Data: []byte("other.txt\x00")}, {Name: tagUTF8Name, Data: []byte("other.txt")}, {Name: tagSize64, Data: make([]byte, 16)}}
	out := replaced(t, b, map[string][]format.Tag{
		"Größe.txt":       append([]format.Tag{a}, append(fake, bb)...),
		"DIR/":            {bb, a},
		"long directory/": {a},
	})
	got := recordTags(t, out)
	for name, want := range map[string]string{
		"Größe.txt":       before["Größe.txt"] + ` TEST:A="a" TEST:B=""`,
		"DIR/":            `TEST:B="" TEST:A="a"`,
		"long directory/": before["long directory/"] + ` TEST:A="a"`,
		"DIR/A.TXT":       "",
	} {
		if got[name] != want {
			t.Errorf("%s: %s, want %s", name, got[name], want)
		}
	}
	// Removing all tags keeps the name tags, and the names.
	out = replaced(t, out, map[string][]format.Tag{"Größe.txt": nil, "DIR/": nil, "long directory/": nil})
	if got := recordTags(t, out); got["Größe.txt"] != before["Größe.txt"] || got["DIR/"] != "" || got["long directory/"] != before["long directory/"] {
		t.Errorf("after removing: %v", got)
	}
	if c, err := contents(t, out); err != nil || c["Größe.txt;0"] != "umlaut" || c["long directory/X;0"] != "x" {
		t.Errorf("contents %v, %v", c, err)
	}

	// Equal tags leave the archive as it is, also when the name tags
	// follow other tags in the record.
	mixed := rewriteCDIR(t, b, func(c *format.CDIR) {
		for i := range c.Entries {
			if e := &c.Entries[i]; len(e.Tags) > 0 {
				e.Tags = append([]format.Tag{a}, e.Tags...)
			}
		}
	})
	r, _ = NewReader(bytes.NewReader(mixed), int64(len(mixed)))
	same := map[string][]format.Tag{}
	for _, f := range r.File {
		same[f.Name] = tags.Record(f).Tags
	}
	if out := replaced(t, mixed, same); !bytes.Equal(out, mixed) {
		t.Error("equal tags changed the archive")
	}

	m := &memFile{b: bytes.Clone(b)}
	w, err := newAppendWriter(m, int64(len(m.b)), nil)
	if err != nil {
		t.Fatal(err)
	}
	f := tags.Source(w).(*Reader).File[0]
	for _, bad := range [][]format.Tag{{{Name: ""}}, {{Name: "0123456789ABCDEF"}}, {{Name: "A\x00B"}}, {{Name: "BIG", Data: make([]byte, format.MaxTagSize+1)}}} {
		if err := tags.Replace(w, f, bad); err == nil {
			t.Errorf("tag %q accepted", bad[0].Name)
		}
	}
	if err := tags.Replace(w, r.File[0], nil); err == nil {
		t.Error("entry of another reader accepted")
	}
	w.Close()
	if err := tags.Replace(w, f, nil); err == nil {
		t.Error("closed writer accepted")
	}
	if !bytes.Equal(m.b, b) {
		t.Error("failed replacements changed the archive")
	}
}

func TestTagsProtected(t *testing.T) {
	b := writeArchive(t, []testFile{{"a.txt", []byte("a")}}, WithDamageProtection(true))
	out := replaced(t, b, map[string][]format.Tag{"a.txt": {{Name: "AIP:Comment", Data: []byte("hi")}}})
	r, err := NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil || !r.Protected || r.Check() != nil {
		t.Fatalf("protection lost: %v", err)
	}
}

// TestTagsUnreadable checks that tags which would make the central
// directory expand more than the reader accepts are refused on Close.
func TestTagsUnreadable(t *testing.T) {
	b := writeArchive(t, []testFile{{"A.TXT", []byte("a")}})
	m := &memFile{b: bytes.Clone(b)}
	w, err := newAppendWriter(m, int64(len(m.b)), nil)
	if err != nil {
		t.Fatal(err)
	}
	zeros := make([]byte, format.MaxTagSize)
	f := tags.Source(w).(*Reader).File[0]
	if err := tags.Replace(w, f, []format.Tag{{Name: "TEST:A", Data: zeros}, {Name: "TEST:B", Data: zeros}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err == nil || !strings.Contains(err.Error(), "central directory too large") {
		t.Errorf("Close: %v", err)
	}
	if !bytes.Equal(m.b, b) {
		t.Error("archive changed")
	}
}

// TestTagsSize64 checks that the size tag of an extended archive survives
// replacing the tags, and that small files do not get one.
func TestTagsSize64(t *testing.T) {
	const huge = 5<<30 + 7
	b := writeArchive(t, []testFile{{"BIG.BIN", []byte("pretend this is large")}, {"SMALL.TXT", []byte("s")}})
	b = rewriteCDIR(t, b, func(c *format.CDIR) {
		for i := range c.Entries {
			if e := &c.Entries[i]; e.Meta.Name.String() == "BIG.BIN" {
				s := make([]byte, 16)
				binary.LittleEndian.PutUint64(s, huge)
				binary.LittleEndian.PutUint64(s[8:], uint64(e.Comp.CompLen))
				e.Size, e.Tags = huge&0xFFFFFFFF, append(e.Tags, format.Tag{Name: tagSize64, Data: s})
			}
		}
	})
	binary.LittleEndian.PutUint16(b[format.FHeadSize+13:], format.NeededLarge)
	size64 := func(b []byte) map[string]string {
		r, err := NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatal(err)
		}
		if !r.extended {
			t.Error("not extended")
		}
		m := map[string]string{}
		for _, f := range r.File {
			m[f.Name] = fmt.Sprintf("%d %x", f.Size, findTag(tags.Record(f).Tags, tagSize64))
		}
		return m
	}
	before := size64(b)
	if !strings.HasPrefix(before["BIG.BIN"], fmt.Sprint(int64(huge))) {
		t.Fatalf("crafted archive: %v", before)
	}
	x := format.Tag{Name: "TEST:X"}
	out := replaced(t, b, map[string][]format.Tag{"BIG.BIN": nil, "SMALL.TXT": {x, {Name: tagSize64, Data: make([]byte, 16)}}})
	if after := size64(out); after["BIG.BIN"] != before["BIG.BIN"] || after["SMALL.TXT"] != "1 " {
		t.Errorf("size tags before %v, after %v", before, after)
	}
	if got := recordTags(t, out)["SMALL.TXT"]; got != `TEST:X=""` {
		t.Errorf("SMALL.TXT tags %s", got)
	}
}
