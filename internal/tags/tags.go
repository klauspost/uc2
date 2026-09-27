// Package tags gives cmd/uc2 access to the central directory records of
// package uc2, for UC2's ~D, ~X and ~R commands, without public API. Package
// uc2 sets the functions in an init; the any values are *uc2.File,
// *uc2.Reader and *uc2.Writer.
package tags

import "github.com/klauspost/uc2/internal/format"

var (
	// Record returns the central directory record of a *uc2.File. It must
	// not be modified.
	Record func(f any) *format.Entry

	// CDIR returns the central directory of a *uc2.Reader, which holds the
	// raw volume label and the creator serial. It must not be modified.
	CDIR func(r any) *format.CDIR

	// Source returns the *uc2.Reader an append *uc2.Writer was opened on,
	// or nil for other writers.
	Source func(w any) any

	// Replace sets the tags of the entry that the append writer w read as f,
	// a *uc2.File of Source(w). The entry keeps its long name tags, which
	// its 8.3 alias is derived from, and the size tag follows the size, so
	// such tags in tags are ignored. Other tags keep their order after the
	// long name tags. Tags equal to the current ones leave w unchanged.
	Replace func(w, f any, tags []format.Tag) error
)
