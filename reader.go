package uc2

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/uc2/internal/dp"
	"github.com/klauspost/uc2/internal/format"
	"github.com/klauspost/uc2/internal/super"
	"github.com/klauspost/uc2/internal/ultra"
)

const maxReadCDIR = 256 << 20

var (
	zero512   = make([]byte, 512)
	readerIDs atomic.Uint64
)

// File is a file or directory revision in an archive.
type File struct {
	FileHeader

	// Revision is 0 for the newest revision of a name, 1 for the previous one, etc.
	Revision int

	// CompressedSize is the size of the compressed stream, excluding its master.
	CompressedSize int64

	r      *Reader
	rec    *format.Entry
	offset int64
}

// Reader provides access to the contents of an archive.
type Reader struct {
	// File lists all directories and all file revisions in archive order.
	// Older revisions of a name precede newer ones.
	File []*File

	Label     string // volume label, "" if none
	Protected bool   // damage protection records are present
	MadeBy    uint16 // version that created the archive, e.g. 202 for UC2 revision 2

	id       uint64 // identifies the reader for Writer.Copy
	ra       io.ReaderAt
	size     int64
	cs       Charset
	fh       format.FHead
	xh       format.XHead
	cdir     *format.CDIR
	compEnd  int64
	extended bool
	pcp      bool
	masters  map[uint32]*format.Master
	cache    masterCache
	fsOnce   sync.Once
	fsIdx    *fsIndex
}

// ReadCloser is a Reader that must be closed.
type ReadCloser struct {
	f *os.File
	Reader
}

// OpenReader opens the named archive.
func OpenReader(name string, opts ...Option) (*ReadCloser, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	rc := &ReadCloser{f: f}
	if err := rc.init(f, fi.Size(), opts); err != nil {
		f.Close()
		return nil, err
	}
	return rc, nil
}

func (rc *ReadCloser) Close() error { return rc.f.Close() }

// NewReader reads an archive of the given size from r.
func NewReader(r io.ReaderAt, size int64, opts ...Option) (*Reader, error) {
	zr := new(Reader)
	if err := zr.init(r, size, opts); err != nil {
		return nil, err
	}
	return zr, nil
}

func unsupported(what string) error { return fmt.Errorf("uc2: %s: %w", what, errors.ErrUnsupported) }

func (r *Reader) init(ra io.ReaderAt, size int64, opts []Option) error {
	cfg := newConfig(opts)
	r.ra, r.size, r.cs, r.id = ra, size, cfg.charset, readerIDs.Add(1)
	if size < 0 {
		return ErrFormat
	}
	var head [format.HeadSize]byte
	n, _ := ra.ReadAt(head[:min(size, int64(len(head)))], 0)
	if n >= 3 && string(head[:3]) == "UE2" {
		return unsupported("archive is encrypted with UltraCrypt")
	}
	if n < len(head) {
		return ErrFormat
	}
	fh, err := format.ParseFHead(head[:])
	if err != nil {
		var spare [format.FHeadSize]byte
		if _, err2 := ra.ReadAt(spare[:], size-format.FHeadSize); err2 != nil {
			return err
		}
		if fh, err = format.ParseFHead(spare[:]); err != nil {
			return err
		}
	}
	xh, _ := format.ParseXHead(head[format.FHeadSize:])
	switch {
	case xh.Needed == format.NeededPCP:
		r.pcp = true
	case xh.Needed == format.NeededLarge:
		r.extended = true
	case xh.Needed > format.NeededLarge:
		return unsupported(fmt.Sprintf("archive needs UltraCompressor %d revision %d", xh.Needed/100, xh.Needed%100))
	}
	r.fh, r.xh, r.Protected, r.MadeBy = fh, xh, fh.Protected, xh.MadeBy

	cdirOff, err := r.abs(xh.Cdir)
	if err != nil || cdirOff < format.HeadSize || cdirOff+format.CompressSize > size {
		return ErrFormat
	}
	var cb [format.CompressSize]byte
	if _, err := ra.ReadAt(cb[:], cdirOff); err != nil {
		return err
	}
	comp := format.ParseCompress(cb[:])
	if d, turbo, err := format.MethodInfo(comp.Method); err != nil || d != 0 || turbo {
		return fmt.Errorf("%w: central directory method %d", ErrFormat, comp.Method)
	}
	start := cdirOff + format.CompressSize
	// UC2 writes a zero length for the central directory; this package the exact one.
	inLen := size - start
	if comp.CompLen != 0 && int64(comp.CompLen) <= inLen {
		inLen = int64(comp.CompLen)
	}
	// Bound the expansion of hostile input; real central directories expand less than 40-fold.
	limit := min(maxReadCDIR, 1<<20+inLen*128)
	dec := ultra.NewReader(io.NewSectionReader(ra, start, inLen), zero512, ultra.Unlimited)
	raw, err := io.ReadAll(io.LimitReader(dec, limit+1))
	if err != nil {
		return fmt.Errorf("%w: central directory: %w", ErrFormat, err)
	}
	if int64(len(raw)) > limit {
		return fmt.Errorf("%w: central directory larger than %d bytes", ErrFormat, limit)
	}
	if format.Fletch(raw) != xh.Fletch {
		return fmt.Errorf("%w: central directory", ErrChecksum)
	}
	// Every record costs memory beyond its name, so charge a fixed amount per
	// record and for resolved paths against a budget tied to the input size.
	budget := int64(16<<20) + 256*inLen
	if r.cdir, err = format.ParseCDIR(raw, int(min(budget/256, math.MaxInt32))); err != nil {
		return err
	}
	r.compEnd = start + int64(comp.CompLen)
	if !r.extended {
		r.compEnd = format.FHeadSize + int64(fh.CompLen)
	}
	r.Label = strings.TrimRight(decodeOEM(bytes.TrimRight(r.cdir.Tail.Label[:], "\x00"), r.cs), " ")
	if r.cdir.Tail.Label[0] == 0 {
		r.Label = ""
	}
	if err := r.buildFiles(budget); err != nil {
		return err
	}
	return r.checkSpans()
}

func (r *Reader) abs(l format.Loc) (int64, error) {
	off := l.Abs()
	if (l.Vol != 1 && !r.extended) || l.Vol == 0 || off < 0 || off > r.size {
		return 0, fmt.Errorf("%w: location %d:%d", ErrFormat, l.Vol, l.Off)
	}
	return off, nil
}

func (r *Reader) buildFiles(budget int64) error {
	c := r.cdir
	r.masters = make(map[uint32]*format.Master, len(c.Masters))
	for i := range c.Masters {
		m := &c.Masters[i]
		if m.Index >= format.FirstMaster {
			r.masters[m.Index] = m
		}
	}
	dirs := map[uint32]string{0: ""}
	type groupKey struct {
		parent uint32
		name   format.Name
	}
	groups := map[groupKey][]*File{}
	r.File = make([]*File, 0, len(c.Entries))
	for i := range c.Entries {
		e := &c.Entries[i]
		parent, ok := dirs[e.Meta.Parent]
		if !ok {
			return fmt.Errorf("%w: undefined parent directory %d", ErrFormat, e.Meta.Parent)
		}
		f := &File{r: r, rec: e}
		f.Modified = format.FromDOS(e.Meta.Date, e.Meta.Time, time.Local)
		f.Attr = Attr(e.Meta.Attr)
		f.ShortName = decodeOEM(e.Meta.Name.Bytes(), r.cs)
		name := elementName(e.Meta.Name, e.Tags, r.cs)
		if budget -= int64(len(parent)+len(name)) + 256; budget < 0 {
			return fmt.Errorf("%w: names too long", ErrFormat)
		}
		if e.Type == format.BoDir {
			f.Name = parent + name + "/"
			if _, dup := dirs[e.Index]; !dup && e.Index != 0 {
				dirs[e.Index] = f.Name
			}
			f.Attr |= AttrDir
			r.File = append(r.File, f)
			continue
		}
		f.Name = parent + name
		f.Size, f.CompressedSize = int64(e.Size), int64(e.Comp.CompLen)
		if s := findTag(e.Tags, tagSize64); s != nil {
			if !r.extended || len(s) != 16 {
				return fmt.Errorf("%w: unexpected %s tag", ErrFormat, tagSize64)
			}
			f.Size = int64(binary.LittleEndian.Uint64(s))
			f.CompressedSize = int64(binary.LittleEndian.Uint64(s[8:]))
			if f.Size < 0 || f.CompressedSize < 0 {
				return ErrFormat
			}
		}
		var err error
		if f.offset, err = r.abs(e.Loc); err != nil {
			return err
		}
		k := groupKey{e.Meta.Parent, e.Meta.Name}
		groups[k] = append(groups[k], f)
		r.File = append(r.File, f)
	}
	for _, revs := range groups {
		name := ""
		for i, f := range revs {
			f.Revision = len(revs) - 1 - i
			if hasNameTag(f.rec.Tags) {
				name = f.Name
			}
		}
		if name != "" {
			for _, f := range revs {
				f.Name = name
			}
		}
	}
	for _, f := range r.File {
		f.stored, f.storedName = storedNameOf(f.rec), f.Name
	}
	return nil
}

// checkSpans rejects compressed streams that overlap or extend past the end
// of the archive, so hostile archives cannot decode or copy the same bytes
// many times over.
func (r *Reader) checkSpans() error {
	type span struct{ off, n int64 }
	var spans []span
	for _, m := range r.masters {
		off, err := r.abs(m.Loc)
		if err != nil {
			return err
		}
		spans = append(spans, span{off, int64(m.Comp.CompLen)})
	}
	for _, f := range r.File {
		if f.rec.Type == format.BoFile && f.Size > 0 {
			spans = append(spans, span{f.offset, f.CompressedSize})
		}
	}
	slices.SortFunc(spans, func(a, b span) int { return cmp.Compare(a.off, b.off) })
	end := int64(0)
	for _, s := range spans {
		if s.off < end || s.n > r.size-s.off {
			return fmt.Errorf("%w: overlapping or truncated compressed data at %d", ErrFormat, s.off)
		}
		end = s.off + s.n
	}
	return nil
}

// storedNameOf returns the on-disk name and tags of e, for copying it verbatim.
func storedNameOf(e *format.Entry) *storedName {
	sn := &storedName{name: e.Meta.Name}
	for _, t := range e.Tags {
		switch t.Name {
		case tagLongName, tagUTF8Name:
			sn.tags = append(sn.tags, t)
		case tagSize64:
		default:
			sn.foreign = append(sn.foreign, t)
		}
	}
	return sn
}

const maxComment = 1 << 20

// Comment returns the archive comment, stored by UC2 as the root file U$~COMM.TXT.
func (r *Reader) Comment() (string, error) {
	var cf *File
	for _, f := range r.File {
		if f.rec.Type == format.BoFile && f.rec.Meta.Parent == 0 && f.Revision == 0 && f.rec.Meta.Name.String() == "U$~COMM.TXT" {
			cf = f
		}
	}
	if cf == nil {
		return "", nil
	}
	if cf.Size > maxComment {
		return "", fmt.Errorf("%w: comment of %d bytes", ErrFormat, cf.Size)
	}
	rc, err := cf.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return decodeOEM(b, r.cs), err
}

// Check verifies the archive structure without decompressing file data: the
// spare header, trailing data and, if present, the damage protection records.
func (r *Reader) Check() error {
	end := r.compEnd
	if r.Protected {
		res, err := dp.Verify(r.ra, end)
		if err != nil {
			return err
		}
		switch {
		case !res.TableOK:
			return fmt.Errorf("%w: damage protection records are damaged", ErrChecksum)
		case len(res.Bad) > 0:
			return fmt.Errorf("%w: %d damaged sectors, first at %d", ErrChecksum, len(res.Bad), res.Bad[0]*512)
		case len(res.ParityBad) > 0:
			return fmt.Errorf("%w: damaged protection records %v", ErrChecksum, res.ParityBad)
		}
		end += dp.AreaSize(end)
	}
	var head, spare [format.FHeadSize]byte
	if _, err := r.ra.ReadAt(spare[:], end); err != nil {
		return fmt.Errorf("%w: missing spare header", ErrFormat)
	}
	if _, err := r.ra.ReadAt(head[:], 0); err != nil {
		return err
	}
	if head != spare {
		return fmt.Errorf("%w: header and spare header differ", ErrFormat)
	}
	if trailing := r.size - end - format.FHeadSize; trailing != 0 && !r.sealed() {
		return fmt.Errorf("%w: %d bytes of trailing data", ErrFormat, trailing)
	}
	return nil
}

// sealed reports whether an UltraSeal is present in the last 1024 bytes.
func (r *Reader) sealed() bool {
	n := min(r.size, 1024)
	b := make([]byte, n)
	r.ra.ReadAt(b, r.size-n)
	return bytes.Contains(b, []byte{0xDB, 0x3A, 0x0F, 0x20, 0x02, 0x20, 0x13, 0x45})
}

type masterCache struct {
	mu    sync.Mutex
	slots map[uint32]*masterSlot
	tick  uint64
}

type masterSlot struct {
	ready chan struct{}
	data  []byte
	err   error
	used  uint64
}

const masterCacheSize = 64

func (r *Reader) master(idx uint32) ([]byte, error) {
	c := &r.cache
	c.mu.Lock()
	if c.slots == nil {
		c.slots = map[uint32]*masterSlot{}
	}
	c.tick++
	s := c.slots[idx]
	if s != nil {
		s.used = c.tick
		c.mu.Unlock()
		<-s.ready
		return s.data, s.err
	}
	if len(c.slots) >= masterCacheSize {
		var old uint32
		var oldest uint64 = 1<<64 - 1
		for k, v := range c.slots {
			if v.used < oldest {
				old, oldest = k, v.used
			}
		}
		delete(c.slots, old)
	}
	s = &masterSlot{ready: make(chan struct{}), used: c.tick}
	c.slots[idx] = s
	c.mu.Unlock()
	s.data, s.err = r.loadMaster(idx)
	close(s.ready)
	if s.err != nil {
		c.mu.Lock()
		if c.slots[idx] == s {
			delete(c.slots, idx)
		}
		c.mu.Unlock()
	}
	return s.data, s.err
}

func (r *Reader) loadMaster(idx uint32) ([]byte, error) {
	m := r.masters[idx]
	if m == nil {
		return nil, fmt.Errorf("%w: missing master %d", ErrFormat, idx)
	}
	prefix := m.Comp.Prefix
	if prefix == format.LegacyPrefix {
		prefix = format.SuperMaster
	}
	if prefix >= format.FirstMaster {
		return nil, unsupported("chained master")
	}
	off, err := r.abs(m.Loc)
	if err != nil {
		return nil, err
	}
	if int64(m.Comp.CompLen) > 2*int64(m.Len)+4096 {
		return nil, fmt.Errorf("%w: master %d of %d bytes has %d compressed bytes", ErrFormat, idx, m.Len, m.Comp.CompLen)
	}
	dst := make([]byte, m.Len)
	src, err := r.stream(m.Comp.Method, prefix, io.NewSectionReader(r.ra, off, int64(m.Comp.CompLen)), int64(m.Len))
	if err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(src, dst); err != nil {
		return nil, fmt.Errorf("%w: master %d: %v", ErrFormat, idx, err)
	}
	src.finish()
	return dst, nil
}

// dict returns the history preceding a stream compressed against prefix.
func (r *Reader) dict(prefix uint32) ([]byte, error) {
	switch prefix {
	case format.SuperMaster:
		return []byte(super.Data), nil
	case format.NoMaster:
		return zero512, nil
	}
	return r.master(prefix)
}

var decoders = sync.Pool{New: func() any { return ultra.NewReader(nil, nil, 0) }}

// stream opens a decoder for a stream with the given method and prefix.
// The returned reader yields original (un-delta'd) bytes and tracks the
// checksum over the delta domain, as UC2 does.
func (r *Reader) stream(method uint16, prefix uint32, src io.Reader, size int64) (*streamReader, error) {
	delta, turbo, err := format.MethodInfo(method)
	if err != nil {
		return nil, unsupported(fmt.Sprintf("compression method %d", method))
	}
	dict, err := r.dict(prefix)
	if err != nil {
		return nil, err
	}
	s := &streamReader{}
	if delta > 0 {
		s.delta = ultra.NewDelta(delta)
		s.hasDelta = true
		if prefix != format.SuperMaster {
			d := ultra.NewDelta(delta)
			dict = bytes.Clone(dict)
			d.Encode(dict)
		}
	}
	if turbo {
		s.src = ultra.NewTurboReader(src, dict, size)
	} else {
		dec := decoders.Get().(*ultra.Reader)
		dec.Reset(src, dict, size)
		s.src, s.dec = dec, dec
	}
	return s, nil
}

type streamReader struct {
	src      io.Reader
	dec      *ultra.Reader
	sum      format.Fletcher
	delta    ultra.Delta
	hasDelta bool
}

func (s *streamReader) Read(p []byte) (int, error) {
	n, err := s.src.Read(p)
	s.sum.Write(p[:n])
	if s.hasDelta {
		s.delta.Decode(p[:n])
	}
	if errors.Is(err, ultra.ErrCorrupt) {
		err = fmt.Errorf("%w: %w", ErrFormat, err)
	}
	return n, err
}

func (s *streamReader) finish() {
	if s.dec != nil {
		s.dec.Reset(nil, nil, 0)
		decoders.Put(s.dec)
		s.dec = nil
	}
	s.src = nil
}

// Open returns a reader for the file contents. The checksum is verified at EOF.
// Open may be called concurrently.
func (f *File) Open() (io.ReadCloser, error) {
	if f.rec.Type == format.BoDir {
		return io.NopCloser(strings.NewReader("")), nil
	}
	if f.r.pcp {
		return nil, unsupported("archive uses a private compression profile")
	}
	e := f.rec
	if f.Size == 0 {
		if e.Fletch != format.Fletch(nil) {
			return nil, ErrChecksum
		}
		return io.NopCloser(strings.NewReader("")), nil
	}
	s, err := f.r.stream(e.Comp.Method, e.Comp.Prefix, io.NewSectionReader(f.r.ra, f.offset, f.CompressedSize), f.Size)
	if err != nil {
		return nil, err
	}
	return &fileReader{s: s, want: e.Fletch, left: f.Size}, nil
}

type fileReader struct {
	mu   sync.Mutex // Close must not recycle the decoder during a Read
	s    *streamReader
	want uint16
	left int64
	err  error
}

func (fr *fileReader) Read(p []byte) (int, error) {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.err != nil {
		return 0, fr.err
	}
	n, err := fr.s.Read(p)
	fr.left -= int64(n)
	if err == io.EOF {
		if fr.left != 0 {
			err = io.ErrUnexpectedEOF
		} else if fr.s.sum.Sum16() != fr.want {
			err = ErrChecksum
		}
	}
	if err != nil {
		fr.err = err
		fr.s.finish()
	}
	return n, err
}

func (fr *fileReader) Close() error {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if fr.err == nil {
		fr.err = errors.New("uc2: read after close")
	}
	fr.s.finish()
	return nil
}
