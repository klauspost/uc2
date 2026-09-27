// Package format implements the on-disk records of UltraCompressor II archives.
// All structures are little-endian and byte-packed.
package format

import (
	"encoding/binary"
	"errors"
)

var ErrFormat = errors.New("uc2: not a valid archive")

const (
	Magic     = 0x1A324355 // "UC2\x1a"
	AMag      = 0x01B2C3D4 // FHEAD.dwComponentLength2 - dwComponentLength
	FHeadSize = 13
	XHeadSize = 16
	HeadSize  = FHeadSize + XHeadSize

	MadeBy      = 202
	Needed      = 200
	NeededPUC   = 202 // masters compressed without the supermaster
	NeededPCP   = 203 // UC 2.3+ private compression profiles
	NeededLarge = 204 // this package's extended mode
)

// Master prefixes.
const (
	SuperMaster  = 0
	NoMaster     = 1
	FirstMaster  = 2
	LegacyPrefix = 0xDEDEDEDE // unset prefix of r1 masters; means SuperMaster
)

// Record types.
const (
	BoDir  = 1
	BoFile = 2
	BoMast = 3
	BoEOL  = 4
)

type FHead struct {
	CompLen   uint32
	Protected bool
}

func ParseFHead(b []byte) (FHead, error) {
	if len(b) < FHeadSize || binary.LittleEndian.Uint32(b) != Magic {
		return FHead{}, ErrFormat
	}
	h := FHead{CompLen: binary.LittleEndian.Uint32(b[4:]), Protected: b[12] != 0}
	if binary.LittleEndian.Uint32(b[8:])-AMag != h.CompLen {
		return FHead{}, ErrFormat
	}
	return h, nil
}

func (h FHead) Append(b []byte) []byte {
	b = binary.LittleEndian.AppendUint32(b, Magic)
	b = binary.LittleEndian.AppendUint32(b, h.CompLen)
	b = binary.LittleEndian.AppendUint32(b, h.CompLen+AMag)
	return append(b, b2u(h.Protected))
}

// Loc is a LOCATION record. Offsets are absolute; in extended mode
// Vol carries the high 32 bits of the offset plus one.
type Loc struct{ Vol, Off uint32 }

func LocOf(off int64) Loc { return Loc{1 + uint32(off>>32), uint32(off)} }

func (l Loc) Abs() int64 { return int64(l.Vol-1)<<32 | int64(l.Off) }

type XHead struct {
	Cdir   Loc
	Fletch uint16
	Busy   uint8
	MadeBy uint16
	Needed uint16
	Dummy  uint8
}

func ParseXHead(b []byte) (XHead, error) {
	if len(b) < XHeadSize {
		return XHead{}, ErrFormat
	}
	le := binary.LittleEndian
	return XHead{
		Cdir:   Loc{le.Uint32(b), le.Uint32(b[4:])},
		Fletch: le.Uint16(b[8:]),
		Busy:   b[10],
		MadeBy: le.Uint16(b[11:]),
		Needed: le.Uint16(b[13:]),
		Dummy:  b[15],
	}, nil
}

func (h XHead) Append(b []byte) []byte {
	le := binary.LittleEndian
	b = le.AppendUint32(b, h.Cdir.Vol)
	b = le.AppendUint32(b, h.Cdir.Off)
	b = le.AppendUint16(b, h.Fletch)
	b = append(b, h.Busy)
	b = le.AppendUint16(b, h.MadeBy)
	b = le.AppendUint16(b, h.Needed)
	return append(b, h.Dummy)
}

type Compress struct {
	CompLen uint32
	Method  uint16
	Prefix  uint32
}

const CompressSize = 10

func ParseCompress(b []byte) Compress {
	le := binary.LittleEndian
	return Compress{le.Uint32(b), le.Uint16(b[4:]), le.Uint32(b[6:])}
}

func (c Compress) Append(b []byte) []byte {
	le := binary.LittleEndian
	b = le.AppendUint32(b, c.CompLen)
	b = le.AppendUint16(b, c.Method)
	return le.AppendUint32(b, c.Prefix)
}

// MethodInfo returns the delta size (0 = none) of a stored method,
// whether it is the turbo method, or an error for methods no UC2 version decodes.
// Delta sizes 9 and 10 (38, 39, 48, 49) overflow UC2's delta state and are rejected.
func MethodInfo(m uint16) (delta int, turbo bool, err error) {
	switch {
	case m >= 1 && m <= 9:
		return 0, false, nil
	case m >= 21 && m <= 29:
		return 1, false, nil
	case m >= 30 && m <= 37:
		return int(m) - 29, false, nil
	case m >= 40 && m <= 47:
		return int(m) - 39, false, nil
	case m == 80:
		return 0, true, nil
	}
	return 0, false, ErrFormat
}

func b2u(b bool) byte {
	if b {
		return 1
	}
	return 0
}
