package main

import (
	_ "embed"
	"strings"
)

// manualText holds chapters 0-8 of the UltraCompressor II 2.4 manual
// (U_MANUAL.TXT) in UTF-8, cut like the documents of UC2 revision 2:
// chapter 2 is replaced by the LGPL notice, chapter 1 no longer binds the
// reader to the old license, and form feeds are gone, the one before each
// chapter with its line.
//
//go:embed manual.txt
var manualText string

// docNames are the documents of UC2 revision 2 that the chapters replace.
var docNames = [...]string{"WHATSNEW", "README", "LICENSE", "BASIC", "MAIN", "BBS", "CONFIG", "BACKGRND", "EXTEND"}

// doc is a document of the viewer: a chapter of the manual, or a file.
type doc struct {
	name  string
	lines []string
}

// manual splits the manual into its chapters, at the lines "N. TITLE"
// underlined with '='.
func manual() []doc {
	var docs []doc
	lines := strings.Split(strings.TrimSuffix(strings.ReplaceAll(manualText, "\r", ""), "\n"), "\n")
	for i, l := range lines {
		if len(l) > 2 && l[0] >= '0' && l[0] <= '9' && l[1] == '.' && l[2] == ' ' && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "==") {
			docs = append(docs, doc{name: docNames[len(docs)]})
		}
		if len(docs) > 0 {
			docs[len(docs)-1].lines = append(docs[len(docs)-1].lines, l)
		}
	}
	return docs
}
