// Package uc2 reads and writes UltraCompressor II archives.
//
// The API mirrors archive/zip. Archives written by this package are readable
// by the original DOS UltraCompressor II (revision 2 and later) unless the input
// requires one of the documented extensions (see README.md).
package uc2

import (
	"errors"
	"runtime"

	"github.com/klauspost/uc2/internal/charset"
	"github.com/klauspost/uc2/internal/format"
)

var (
	ErrFormat   = format.ErrFormat
	ErrChecksum = errors.New("uc2: checksum error")
	// ErrLocked is returned by NewAppendWriter when another append writer holds the archive.
	ErrLocked = errors.New("uc2: archive is being updated by another writer")
)

// Level selects the compression effort. The values are the UC2 method numbers.
type Level int

const (
	Fast       Level = 2 // UC2 -TF
	Normal     Level = 3 // UC2 -TN, the default
	Tight      Level = 4 // UC2 -TT, with multimedia (delta) detection
	SuperTight Level = 5 // UC2 -TST, with multimedia detection
)

// Attr is a DOS attribute byte.
type Attr uint8

const (
	AttrReadOnly Attr = 0x01
	AttrHidden   Attr = 0x02
	AttrSystem   Attr = 0x04
	AttrDir      Attr = 0x10
	AttrArchive  Attr = 0x20
)

// Charset is a single-byte OEM code page used for 8.3 names, long names and
// the archive comment. The code pages of golang.org/x/text/encoding/charmap satisfy it.
type Charset interface {
	DecodeByte(b byte) rune
	EncodeRune(r rune) (b byte, ok bool)
}

// CP437 (the default) and CP850 are the common DOS code pages.
var (
	CP437 Charset = charset.CP437
	CP850 Charset = charset.CP850
)

type config struct {
	level       Level
	concurrency int
	protect     *bool
	charset     Charset
}

func newConfig(opts []Option) config {
	c := config{level: Normal, concurrency: runtime.GOMAXPROCS(0), charset: CP437}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// Option configures a Reader or Writer.
type Option func(*config)

// WithLevel sets the compression level of a Writer.
func WithLevel(l Level) Option {
	return func(c *config) {
		if l >= Fast && l <= SuperTight {
			c.level = l
		}
	}
}

// WithConcurrency sets the number of compression goroutines of a Writer.
// Values < 1 select GOMAXPROCS. The output does not depend on the value.
func WithConcurrency(n int) Option {
	return func(c *config) {
		if n < 1 {
			n = runtime.GOMAXPROCS(0)
		}
		c.concurrency = n
	}
}

// WithDamageProtection adds (or, for append writers, removes) UC2 damage
// protection records, which allow repairing damaged sectors.
func WithDamageProtection(on bool) Option {
	return func(c *config) { c.protect = &on }
}

// WithCharset sets the code page used to decode and encode names. Default CP437.
func WithCharset(cs Charset) Option {
	return func(c *config) {
		if cs != nil {
			c.charset = cs
		}
	}
}
