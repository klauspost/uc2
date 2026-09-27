package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/klauspost/uc2/internal/charset"
)

func TestManual(t *testing.T) {
	docs := manual()
	if len(docs) != 9 {
		t.Fatalf("%d chapters", len(docs))
	}
	// The line counts of the chapters cut as UC2 revision 2's documents,
	// which were checked in its viewer; chapter 1 lost 2 lines.
	counts := []int{119, 686, 55, 87, 835, 179, 461, 330, 747}
	paras := []string{"ABC", "ABCDEFGHIJ", "ABC", "ABCDZ", "ABCDEFZ", "ABCZ", "ABCDE", "ABCD", "ABCDEFGZ"}
	for i, d := range docs {
		if !strings.HasPrefix(d.lines[0], fmt.Sprintf("%d. ", i)) || len(d.lines) != counts[i] {
			t.Errorf("chapter %d: %d lines from %q", i, len(d.lines), d.lines[0])
		}
		var got strings.Builder
		for c := byte('A'); c <= 'Z'; c++ {
			if j := anchor(d.lines, c); j >= 0 {
				got.WriteString(string(c))
				if heading(d.lines, j, byte('0'+i)) != d.lines[j] {
					t.Errorf("chapter %d: title at %c is %q", i, c, heading(d.lines, j, byte('0'+i)))
				}
			}
		}
		if got.String() != paras[i] {
			t.Errorf("chapter %d: paragraphs %s, want %s", i, got.String(), paras[i])
		}
		for j, l := range d.lines {
			// The viewer colors exactly the headings and their underlines.
			if isHeading(fit(l, 80)) != (para(d.lines, j) && l[0] == byte('0'+i) || strings.HasPrefix(l, "===")) {
				t.Errorf("chapter %d line %d: heading %v: %q", i, j, isHeading(fit(l, 80)), l)
			}
			if utf8.RuneCountInString(l) > 72 || strings.ContainsFunc(l, func(r rune) bool { return r < ' ' || r == 0x7f }) {
				t.Errorf("chapter %d line %d: %q", i, j, l)
			}
		}
	}

	// The LGPL notice keeps the structure of the manual.
	lic := docs[2].lines
	for j, l := range lic {
		if para(lic, j) && len(lic[j+1]) != len(l) || strings.ContainsFunc(l, func(r rune) bool { return r >= utf8.RuneSelf }) {
			t.Errorf("license line %d: %q", j, l)
		}
	}
	text := strings.Join(lic, "\n")
	for _, s := range []string{"2. LICENSE (the license agreement, warranty, etc.)", "2.A THE ULTRACOMPRESSOR II SOURCE CODE", "2.B THE GO PORT",
		"2.C NO WARRANTY", "Lesser General Public License, version 3 (LGPL-3.0)", "There is NO WARRANTY.", "WITHOUT ANY WARRANTY",
		"https://github.com/klauspost/uc2"} {
		if !strings.Contains(text, s) {
			t.Errorf("license lacks %q", s)
		}
	}
	if s := strings.Join(docs[1].lines, "\n"); strings.Contains(s, "license agreement") || !strings.Contains(s, "More details on this are in chapter 2.\n\n") {
		t.Error("chapter 1 still binds the reader to the AIPNL license")
	}

	// The other chapters are the manual, if it is around.
	b, err := os.ReadFile(filepath.Join("..", "..", "_reference", "U_MANUAL.TXT"))
	if err != nil {
		t.Skip(err)
	}
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(charset.CP437.DecodeByte(c))
	}
	lines := strings.Split(strings.TrimSuffix(sb.String(), "\n"), "\n")
	// Chapters without the form feed before the next one, and with the
	// others as empty lines.
	var ref [][]string
	for i, l := range lines {
		if len(l) > 2 && l[0] >= '0' && l[0] <= '9' && l[1] == '.' && l[2] == ' ' && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "==") {
			if n := len(ref); n > 0 && lines[i-1] == "\f" {
				ref[n-1] = ref[n-1][:len(ref[n-1])-1]
			}
			ref = append(ref, nil)
		}
		if l == "\f" {
			l = ""
		}
		ref[len(ref)-1] = append(ref[len(ref)-1], l)
	}
	i := slices.Index(ref[1], "period of 30 days. More details on this are in chapter 2. Chapter 2 also")
	ref[1] = slices.Replace(ref[1], i, i+3, "period of 30 days. More details on this are in chapter 2.")
	for i, d := range docs {
		if i != 2 && !slices.Equal(d.lines, ref[i]) {
			t.Errorf("chapter %d differs from U_MANUAL.TXT", i)
		}
	}
}
