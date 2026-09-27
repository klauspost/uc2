package uc2

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/uc2/internal/format"
)

var errBadName = errors.New("uc2: invalid name")

const (
	maxNameLen = 64 << 10 // longest path element accepted
	maxLongN   = 259      // longest name UC 2.37b copies into its MAX_PATH buffer
)

// splitName validates a slash-separated archive path and returns its elements.
func splitName(name string) ([]string, bool, error) {
	dir := strings.HasSuffix(name, "/")
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.HasPrefix(name, "/") {
		return nil, false, fmt.Errorf("%w: %q", errBadName, name)
	}
	parts := strings.Split(name, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || len(p) > maxNameLen || strings.ContainsAny(p, "\\\x00") || !utf8.ValidString(p) {
			return nil, false, fmt.Errorf("%w: %q", errBadName, name)
		}
	}
	return parts, dir, nil
}

// Device names DOS (and UC2's LLIO.CPP) treat specially, with or without extension.
var deviceNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true, "CLOCK$": true,
	"EMMXXXX0": true, "XMSXXXX0": true, "CACHE$$$": true, "386LOAD$": true,
	"MS$MOUSE": true, "DBLSSYS$": true, "MVPROAS": true, "386MAX$": true,
}

func init() {
	for i := '1'; i <= '9'; i++ {
		deviceNames["COM"+string(i)] = true
		deviceNames["LPT"+string(i)] = true
	}
}

// safeAlias reports whether n can be stored verbatim as a DOS 8.3 name.
func safeAlias(n format.Name) bool {
	base, ext := n.Base(), n.Ext()
	if len(base) == 0 || base[0] == 0xE5 || deviceNames[string(base)] || strings.HasPrefix(string(n[:]), "U$~") {
		return false
	}
	valid := func(b []byte, width int) bool {
		for _, c := range b {
			if c < 0x20 || strings.IndexByte(`\/:*?"<>|. +,;=[]`, c) >= 0 {
				return false
			}
		}
		return len(b) <= width
	}
	return valid(base, 8) && valid(ext, 3)
}

// parseAlias converts a "NAME.EXT" hint in charset cs to an FCB name.
func parseAlias(s string, cs Charset) (format.Name, bool) {
	b, ok := encodeOEM(s, cs)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A' // ASCII only: other bytes are OEM code points
		}
	}
	if !ok {
		return format.Name{}, false
	}
	base, ext, _ := strings.Cut(string(b), ".")
	if strings.Contains(ext, ".") {
		return format.Name{}, false
	}
	n, ok := format.MakeName([]byte(base), []byte(ext))
	return n, ok && safeAlias(n)
}

// encodeOEM encodes s in cs; unmappable runes become '_' and ok is false.
func encodeOEM(s string, cs Charset) ([]byte, bool) {
	b := make([]byte, 0, len(s))
	ok := true
	for _, r := range s {
		c, good := cs.EncodeRune(r)
		if !good {
			c, ok = '_', false
		}
		b = append(b, c)
	}
	return b, ok
}

// genAlias derives a unique 8.3 alias from a long name with the Win95 FAT
// basis-name algorithm, so the alias matches what the file system generates
// when UC 2.37b restores the long name (a mismatch can hang UC 2.37b).
// tails records the highest ~N tried per stem, so filling a directory with
// similar names is linear.
func genAlias(long string, cs Charset, taken func(format.Name) bool, tails map[string]int) format.Name {
	lossy := false
	var b []byte
	for _, r := range strings.ToUpper(long) {
		switch {
		case r == ' ':
			lossy = true
		case r == '.' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r < 0x80 && strings.ContainsRune("$%'-_@~`!(){}^#&", r):
			b = append(b, byte(r))
		default:
			c, ok := cs.EncodeRune(r)
			if !ok || c < 0x80 {
				c, lossy = '_', true
			}
			b = append(b, c)
		}
	}
	trimmed := bytes.TrimLeft(b, ".")
	lossy = lossy || len(trimmed) != len(b)
	base, ext := trimmed, []byte(nil)
	if i := bytes.IndexByte(trimmed, '.'); i >= 0 {
		base = trimmed[:i]
		j := bytes.LastIndexByte(trimmed, '.')
		ext = trimmed[j+1:]
		lossy = lossy || i != j
	}
	if len(base) > 8 || len(ext) > 3 {
		lossy = true
		ext = ext[:min(len(ext), 3)]
	}
	if len(base) == 0 {
		base, lossy = []byte("_"), true
	}
	if base[0] == 0xE5 || bytes.HasPrefix(base, []byte("U$~")) {
		base[0], lossy = '_', true
	}
	if !lossy {
		if n, _ := format.MakeName(base, ext); safeAlias(n) && !taken(n) {
			return n
		}
	}
	for i := 1; ; i++ {
		suffix := "~" + strconv.Itoa(i)
		stem := base[:min(len(base), 8-len(suffix))]
		key := string(stem) + "." + string(ext)
		if t := tails[key]; t >= i {
			i = t
			continue
		}
		tails[key] = i
		if n, _ := format.MakeName(append(append([]byte{}, stem...), suffix...), ext); !taken(n) && safeAlias(n) {
			return n
		}
	}
}

// nameTags returns the tags storing long name for an entry with alias n: the
// UC 2.37b long name if it differs from the alias and fits UC 2.37b, and a
// UTF-8 copy if the long name cannot be represented exactly in the code page.
func nameTags(long string, n format.Name, cs Charset) []format.Tag {
	if long == decodeOEM(n.Bytes(), cs) {
		return nil
	}
	oem, exact := encodeOEM(long, cs)
	for i, c := range oem {
		if c < 0x20 || strings.IndexByte(`"*:<>?\|`, c) >= 0 {
			oem[i], exact = '_', false
		}
	}
	var tags []format.Tag
	if len(oem) <= maxLongN {
		tags = append(tags, format.Tag{Name: tagLongName, Data: append(oem, 0)})
	} else {
		exact = false
	}
	if !exact || !isASCII(long) {
		tags = append(tags, format.Tag{Name: tagUTF8Name, Data: []byte(long)})
	}
	return tags
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
