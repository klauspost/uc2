package format

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	metaSize   = 22
	extSize    = 21
	tailSize   = 17
	MaxTagSize = 1000000 // UC2 treats larger tags as damage
	TagNameMax = 15
	MinMaster  = 512
	MaxMaster  = 62976 // largest multiple of 512 below UC2's 63000 master read cap
)

// Meta is an OSMETA record without its tag flag, which is derived from Entry.Tags.
type Meta struct {
	Parent uint32
	Attr   uint8
	Time   uint16
	Date   uint16
	Name   Name
	Hidden uint8
}

type Tag struct {
	Name string
	Data []byte
}

// Entry is a BO_DIR or BO_FILE record.
type Entry struct {
	Type   uint8
	Meta   Meta
	Index  uint32 // directories
	Size   uint32 // files
	Fletch uint16
	Comp   Compress
	Loc    Loc
	Tags   []Tag
}

type Master struct {
	Index, Key, RefLen, RefCtr uint32
	Len, Fletch                uint16
	Comp                       Compress
	Loc                        Loc
}

// Tail is the XTAIL record.
type Tail struct {
	Beta, Lock uint8
	Serial     uint32
	Label      [11]byte
}

type CDIR struct {
	Entries []Entry
	Masters []Master
	Tail    Tail
	Serial  uint32 // creator serial following XTAIL
}

type cdirReader struct {
	b   []byte
	err bool
}

func (r *cdirReader) next(n int) []byte {
	if len(r.b) < n {
		r.err = true
		r.b = nil
		return make([]byte, n)
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

// ParseCDIR parses a raw (decompressed) central directory with at most
// maxRecords records. Tag data aliases raw. A truncated XTAIL or serial reads
// as zeros, as in UC2.
func ParseCDIR(raw []byte, maxRecords int) (*CDIR, error) {
	le := binary.LittleEndian
	r := &cdirReader{b: raw}
	c := &CDIR{}
	for {
		if len(r.b) == 0 {
			return nil, fmt.Errorf("%w: central directory ends without EOL", ErrFormat)
		}
		if len(c.Entries)+len(c.Masters) >= maxRecords {
			return nil, fmt.Errorf("%w: too many records", ErrFormat)
		}
		typ := r.next(1)[0]
		switch typ {
		case BoDir, BoFile:
			e := Entry{Type: typ}
			m := r.next(metaSize)
			e.Meta = Meta{Parent: le.Uint32(m), Attr: m[4], Time: le.Uint16(m[5:]), Date: le.Uint16(m[7:]), Hidden: m[20]}
			copy(e.Meta.Name[:], m[9:20])
			hasTags := m[21] != 0
			if typ == BoDir {
				e.Index = le.Uint32(r.next(4))
			} else {
				f := r.next(6 + CompressSize + 8)
				e.Size, e.Fletch = le.Uint32(f), le.Uint16(f[4:])
				e.Comp = ParseCompress(f[6:])
				e.Loc = Loc{le.Uint32(f[16:]), le.Uint32(f[20:])}
			}
			for hasTags && !r.err {
				h := r.next(extSize)
				size := le.Uint32(h[16:])
				if size > MaxTagSize {
					return nil, fmt.Errorf("%w: tag size %d", ErrFormat, size)
				}
				name, _, _ := bytes.Cut(h[:16], []byte{0})
				e.Tags = append(e.Tags, Tag{Name: string(name), Data: r.next(int(size))})
				hasTags = h[20] != 0
			}
			c.Entries = append(c.Entries, e)
		case BoMast:
			m := r.next(20 + CompressSize + 8)
			c.Masters = append(c.Masters, Master{
				Index: le.Uint32(m), Key: le.Uint32(m[4:]), RefLen: le.Uint32(m[8:]), RefCtr: le.Uint32(m[12:]),
				Len: le.Uint16(m[16:]), Fletch: le.Uint16(m[18:]),
				Comp: ParseCompress(m[20:]),
				Loc:  Loc{le.Uint32(m[30:]), le.Uint32(m[34:])},
			})
		case BoEOL:
			var t [tailSize + 4]byte
			copy(t[:], r.b)
			c.Tail = Tail{Beta: t[0], Lock: t[1], Serial: le.Uint32(t[2:])}
			copy(c.Tail.Label[:], t[6:17])
			c.Serial = le.Uint32(t[17:])
			return c, nil
		default:
			return nil, fmt.Errorf("%w: unknown record type %d", ErrFormat, typ)
		}
		if r.err {
			return nil, fmt.Errorf("%w: truncated central directory", ErrFormat)
		}
	}
}

// Append marshals c: entries in slice order, masters, EOL, XTAIL and serial.
func (c *CDIR) Append(b []byte) []byte {
	le := binary.LittleEndian
	for i := range c.Entries {
		e := &c.Entries[i]
		b = append(b, e.Type)
		b = le.AppendUint32(b, e.Meta.Parent)
		b = append(b, e.Meta.Attr)
		b = le.AppendUint16(b, e.Meta.Time)
		b = le.AppendUint16(b, e.Meta.Date)
		b = append(b, e.Meta.Name[:]...)
		b = append(b, e.Meta.Hidden, b2u(len(e.Tags) > 0))
		if e.Type == BoDir {
			b = le.AppendUint32(b, e.Index)
		} else {
			b = le.AppendUint32(b, e.Size)
			b = le.AppendUint16(b, e.Fletch)
			b = e.Comp.Append(b)
			b = appendLoc(b, e.Loc)
		}
		for j, t := range e.Tags {
			var name [16]byte
			copy(name[:TagNameMax], t.Name)
			b = append(b, name[:]...)
			b = le.AppendUint32(b, uint32(len(t.Data)))
			b = append(b, b2u(j < len(e.Tags)-1))
			b = append(b, t.Data...)
		}
	}
	for _, m := range c.Masters {
		b = append(b, BoMast)
		b = le.AppendUint32(b, m.Index)
		b = le.AppendUint32(b, m.Key)
		b = le.AppendUint32(b, m.RefLen)
		b = le.AppendUint32(b, m.RefCtr)
		b = le.AppendUint16(b, m.Len)
		b = le.AppendUint16(b, m.Fletch)
		b = m.Comp.Append(b)
		b = appendLoc(b, m.Loc)
	}
	b = append(b, BoEOL, c.Tail.Beta, c.Tail.Lock)
	b = le.AppendUint32(b, c.Tail.Serial)
	b = append(b, c.Tail.Label[:]...)
	return le.AppendUint32(b, c.Serial)
}

func appendLoc(b []byte, l Loc) []byte {
	b = binary.LittleEndian.AppendUint32(b, l.Vol)
	return binary.LittleEndian.AppendUint32(b, l.Off)
}

// ValidMasterMethod reports whether UC2 r2 can decode and safely reuse a master stored with method m.
func ValidMasterMethod(m uint16) bool {
	d, turbo, err := MethodInfo(m)
	return err == nil && !turbo && d <= 8
}

// Validate checks the invariants UC2 r2 relies on when reading and updating an archive.
// Masters must precede nothing in particular; entries must be in canonical order.
func (c *CDIR) Validate(extended bool) error {
	bad := func(f string, a ...any) error { return fmt.Errorf("uc2: invalid central directory: "+f, a...) }
	dirs := map[uint32]bool{0: true}
	type key struct {
		parent uint32
		name   Name
	}
	names := map[key]uint8{}
	doneFiles := map[uint32]bool{} // directories whose file run has ended
	masters := map[uint32]bool{}
	for _, m := range c.Masters {
		switch {
		case m.Index < FirstMaster || masters[m.Index]:
			return bad("master index %d", m.Index)
		case m.Comp.Prefix != SuperMaster && m.Comp.Prefix != NoMaster:
			return bad("master %d prefix %d", m.Index, m.Comp.Prefix)
		case m.Len%512 != 0 || m.Len < MinMaster || m.Len > MaxMaster:
			return bad("master %d length %d", m.Index, m.Len)
		case !ValidMasterMethod(m.Comp.Method):
			return bad("master %d method %d", m.Index, m.Comp.Method)
		case !extended && m.Loc.Vol != 1:
			return bad("master %d volume %d", m.Index, m.Loc.Vol)
		}
		masters[m.Index] = true
	}
	var runParent uint32
	var runName Name
	inRun := false
	for i := range c.Entries {
		e := &c.Entries[i]
		if !dirs[e.Meta.Parent] {
			return bad("parent %d not defined before use", e.Meta.Parent)
		}
		for _, t := range e.Tags {
			if t.Name == "" || len(t.Name) > TagNameMax || bytes.IndexByte([]byte(t.Name), 0) >= 0 || len(t.Data) > MaxTagSize {
				return bad("tag %q", t.Name)
			}
		}
		k := key{e.Meta.Parent, e.Meta.Name}
		if e.Type == BoDir {
			if inRun {
				doneFiles[runParent] = true
				inRun = false
			}
			if e.Index == 0 || e.Index >= 1<<31 || dirs[e.Index] {
				return bad("directory index %d", e.Index)
			}
			if names[k] != 0 {
				return bad("duplicate name %q", e.Meta.Name)
			}
			dirs[e.Index] = true
			names[k] = BoDir
			continue
		}
		if e.Type != BoFile {
			return bad("record type %d", e.Type)
		}
		newName := !inRun || runParent != e.Meta.Parent || runName != e.Meta.Name
		if !inRun || runParent != e.Meta.Parent {
			if inRun {
				doneFiles[runParent] = true
			}
			if doneFiles[e.Meta.Parent] {
				return bad("files of directory %d are not contiguous", e.Meta.Parent)
			}
			inRun, runParent = true, e.Meta.Parent
		}
		if newName && names[k] != 0 {
			return bad("duplicate or non-contiguous name %q", e.Meta.Name)
		}
		runName = e.Meta.Name
		names[k] = BoFile
		switch {
		case e.Comp.Prefix < FirstMaster || !masters[e.Comp.Prefix]:
			return bad("file %q uses master %d", e.Meta.Name, e.Comp.Prefix)
		case !extended && e.Loc.Vol != 1:
			return bad("file %q volume %d", e.Meta.Name, e.Loc.Vol)
		}
		if _, turbo, err := MethodInfo(e.Comp.Method); err != nil || turbo {
			return bad("file %q method %d", e.Meta.Name, e.Comp.Method)
		}
	}
	return nil
}
