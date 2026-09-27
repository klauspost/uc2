package uc2

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/uc2/internal/format"
	"github.com/klauspost/uc2/internal/safename"
)

// Tags managed by this package; all others are preserved but not exposed.
const (
	tagLongName = "AIP:Win95 LongN" // UC 2.37b long name, OEM code page, NUL terminated
	tagUTF8Name = "UC2X:UTF8Name"   // extension: UTF-8 name element
	tagSize64   = "UC2X:Size64"     // extension: u64 size, u64 compressed size
)

func findTag(tags []format.Tag, name string) []byte {
	for _, t := range tags {
		if t.Name == name {
			return t.Data
		}
	}
	return nil
}

func decodeOEM(b []byte, cs Charset) string {
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(cs.DecodeByte(c))
	}
	return sb.String()
}

// hasNameTag reports whether the record carries an explicit long name.
func hasNameTag(tags []format.Tag) bool {
	l := findTag(tags, tagLongName)
	return len(findTag(tags, tagUTF8Name)) > 0 || (len(l) > 0 && l[0] != 0)
}

// elementName resolves the display name of a record: UTF8Name, then LongN, then the 8.3 name.
func elementName(n format.Name, tags []format.Tag, cs Charset) string {
	// UC 2.37b sometimes stores empty long names; they mean "no long name".
	l, _, _ := bytes.Cut(findTag(tags, tagLongName), []byte{0})
	switch u := findTag(tags, tagUTF8Name); {
	case len(u) > 0 && len(u) <= maxNameLen && utf8.Valid(u):
		return sanitizeElement(string(u))
	case len(l) > 0 && len(l) <= maxNameLen:
		return sanitizeElement(decodeOEM(l, cs))
	}
	return sanitizeElement(decodeOEM(n.Bytes(), cs))
}

// sanitizeElement makes a single path element safe: no separators or control
// characters (which could drive a terminal), not empty, "." or "..".
func sanitizeElement(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
			return '_'
		}
		return r
	}, s)
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	// Windows drops trailing dots and spaces, which could alias another name.
	if t := strings.TrimRight(s, ". "); len(t) < len(s) {
		s = t + strings.Repeat("_", len(s)-len(t))
	}
	// Names that reach a .git directory only through a file system alias.
	if safename.DotGit(s) && !strings.EqualFold(s, ".git") {
		s = "_" + s
	}
	return s
}

func isInternalName(n format.Name) bool { return bytes.HasPrefix(n[:], []byte("U$~")) }
