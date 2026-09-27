package uc2

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/uc2/internal/dp"
	"github.com/klauspost/uc2/internal/format"
	"github.com/klauspost/uc2/internal/ultra"
)

const (
	fragSize   = 1 << 20  // larger entries are compressed in fragments of this size, in parallel
	smallMax   = fragSize // entries up to this size are batched for grouping into masters
	batchMax   = 32 << 20 // batched bytes before a batch is compressed
	maxCompat  = 0x7FFFFFFF
	maxRawCDIR = 100000000 // UC2's central directory decompression limit
	maxVMEM    = 60000000  // estimated UC2 memory for the central directory
	maxDOSDir  = 63
	maxDOSPath = 79
	// Keeps ~N aliases below ~9999999; the central directory limit is lower anyway.
	maxDirEntries = 1 << 22
)

var (
	errClosed  = errors.New("uc2: writer is closed")
	errDirFull = errors.New("uc2: too many entries in one directory")
)

// Writer writes an archive. It is not safe for concurrent use, but compresses
// in parallel internally. The output does not depend on the concurrency.
type Writer struct {
	w     io.WriteSeeker
	bw    *bufio.Writer
	start int64
	off   int64 // bytes written, relative to start
	cfg   config
	err   error
	dp    *dp.Sum

	started, closed bool
	root            *wdir
	deep            bool // an 8.3 path exceeds DOS limits
	cur             *entryWriter
	batch           []*entryWriter
	batchN          int
	masters         []*wmaster
	nextMaster      uint32
	zero            *wmaster
	copied          map[copyKey]*wmaster
	comment         *string
	tail            format.Tail
	app             *appendState
	changed         bool
	committed       bool

	fifo     []*job
	inflight int64
	tokens   chan struct{}
	stream   streamState
}

type wdir struct {
	parent   *wdir
	name     format.Name
	tags     []format.Tag
	meta     format.Meta
	children []*wdir
	groups   []*wgroup
	byLong   map[string]any
	byAlias  map[format.Name]any
	tails    map[string]int
	path83   int // length of the 8.3 path "DIR\SUB"
	index    uint32
}

type wgroup struct {
	name format.Name
	long string
	revs []*wrev
}

type wrev struct {
	meta   format.Meta
	tags   []format.Tag
	size   int64
	fletch uint16
	method uint16
	master *wmaster
	off    int64
	comp   int64
}

type wmaster struct {
	index   uint32
	key     uint32
	length  int
	method  uint16
	prefix  uint32
	off     int64
	comp    int64
	refLen  uint32
	refCtr  uint32
	content []byte
	dict    func() *ultra.Dict
}

type copyKey struct {
	reader uint64
	idx    uint32
}

// NewWriter returns a Writer writing an archive to w, starting at its current
// position; offsets inside the archive are relative to it. The header is
// rewritten at Close, so w must allow overwriting (not be opened for appending).
func NewWriter(w io.WriteSeeker, opts ...Option) *Writer {
	cfg := newConfig(opts)
	return &Writer{
		w: w, cfg: cfg, nextMaster: format.FirstMaster,
		root:   &wdir{byLong: map[string]any{}, byAlias: map[format.Name]any{}},
		tokens: make(chan struct{}, cfg.concurrency),
		copied: map[copyKey]*wmaster{},
	}
}

func (w *Writer) begin() error {
	if w.closed {
		return errClosed
	}
	if w.started || w.err != nil {
		return w.err
	}
	w.started = true
	if w.app != nil {
		return w.beginAppend()
	}
	w.start, w.err = w.w.Seek(0, io.SeekCurrent)
	if w.err != nil {
		return w.err
	}
	w.bw = bufio.NewWriterSize(w.w, 1<<20)
	if w.cfg.protect != nil && *w.cfg.protect {
		w.dp = new(dp.Sum)
	}
	w.write(make([]byte, format.HeadSize))
	return w.err
}

func (w *Writer) write(b []byte) {
	if w.err != nil {
		return
	}
	n, err := w.bw.Write(b)
	w.off += int64(n)
	w.err = err
	if w.dp != nil {
		w.dp.Write(b[:n])
	}
}

func (w *Writer) fail(err error) error {
	if w.err == nil {
		w.err = err
	}
	return w.err
}

// Create adds a file with the current time and the archive attribute.
func (w *Writer) Create(name string) (io.Writer, error) {
	return w.CreateHeader(&FileHeader{Name: name, Modified: time.Now(), Attr: AttrArchive})
}

// CreateHeader adds a file or directory (Name ending in "/"). Adding an existing
// name again adds a newer revision. The returned writer is valid until the
// next call to CreateHeader, Copy or Close.
func (w *Writer) CreateHeader(fh *FileHeader) (io.Writer, error) {
	ew, err := w.create(fh, nil)
	switch {
	case err != nil:
		return nil, err
	case ew == nil:
		return dirWriter{}, nil
	}
	return ew, nil
}

type dirWriter struct{}

func (dirWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		return 0, errors.New("uc2: write to a directory")
	}
	return 0, nil
}

// storedName carries the on-disk name of an entry being copied.
type storedName struct {
	name    format.Name
	tags    []format.Tag // name tags, verbatim
	foreign []format.Tag
}

func (w *Writer) create(fh *FileHeader, sn *storedName) (*entryWriter, error) {
	if err := w.begin(); err != nil {
		return nil, err
	}
	if err := w.closeEntry(); err != nil {
		return nil, err
	}
	if sn == nil && fh.stored != nil && fh.Name == fh.storedName {
		sn = fh.stored
	}
	w.changed = true
	parts, isDir, err := splitName(fh.Name)
	if err != nil {
		return nil, err
	}
	date, tm := format.DOSTime(fh.Modified.In(time.Local))
	meta := format.Meta{Attr: uint8(fh.Attr), Date: date, Time: tm}
	parent, err := w.dirFor(parts[:len(parts)-1], meta)
	if err != nil {
		return nil, err
	}
	long := parts[len(parts)-1]
	if isDir {
		meta.Attr |= uint8(AttrDir)
		d, err := w.addDir(parent, long, fh.ShortName, sn, meta)
		if err == nil {
			d.meta = meta
		}
		return nil, err
	}
	g, err := w.addGroup(parent, long, fh.ShortName, sn)
	if err != nil {
		return nil, err
	}
	rev := &wrev{meta: meta, tags: w.tagsFor(long, g.name, sn)}
	g.revs = append(g.revs, rev)
	w.checkDepth(parent, g.name)
	w.cur = &entryWriter{w: w, rev: rev, name: g.name}
	return w.cur, nil
}

// tagsFor returns the tags of an entry with long name long and alias n.
// Copied entries keep their tags; if their alias changed and they had no long
// name, the old name is kept as a long name (never as a UTF-8 name, which would
// freeze a code page guess).
func (w *Writer) tagsFor(long string, n format.Name, sn *storedName) []format.Tag {
	if sn == nil {
		return nameTags(long, n, w.cfg.charset)
	}
	tags := sn.tags
	if n != sn.name && !hasNameTag(sn.tags) {
		tags = nil
		for _, t := range nameTags(long, n, w.cfg.charset) {
			if t.Name == tagLongName {
				tags = []format.Tag{t}
			}
		}
	}
	return append(append([]format.Tag(nil), tags...), sn.foreign...)
}

// dirFor returns the directory for path elements, creating implicit directories.
func (w *Writer) dirFor(parts []string, meta format.Meta) (*wdir, error) {
	d := w.root
	for _, p := range parts {
		switch n := d.byLong[p].(type) {
		case *wdir:
			d = n
		case nil:
			m := meta
			m.Attr = uint8(AttrDir)
			nd, err := w.addDir(d, p, "", nil, m)
			if err != nil {
				return nil, err
			}
			d = nd
		default:
			return nil, fmt.Errorf("%w: %q is a file", errBadName, p)
		}
	}
	return d, nil
}

func (w *Writer) pickAlias(d *wdir, long, hint string, sn *storedName) format.Name {
	free := func(n format.Name) bool { return d.byAlias[n] == nil }
	if sn != nil && free(sn.name) && (safeAlias(sn.name) || isInternalName(sn.name)) {
		return sn.name
	}
	if hint != "" {
		if n, ok := parseAlias(hint, w.cfg.charset); ok && free(n) {
			return n
		}
	}
	if d.tails == nil {
		d.tails = map[string]int{}
	}
	return genAlias(long, w.cfg.charset, func(n format.Name) bool { return !free(n) }, d.tails)
}

func (w *Writer) addDir(parent *wdir, long, hint string, sn *storedName, meta format.Meta) (*wdir, error) {
	switch n := parent.byLong[long].(type) {
	case *wdir:
		return n, nil
	case *wgroup:
		return nil, fmt.Errorf("%w: %q is a file", errBadName, long)
	}
	if len(parent.byAlias) >= maxDirEntries {
		return nil, errDirFull
	}
	d := &wdir{parent: parent, meta: meta, byLong: map[string]any{}, byAlias: map[format.Name]any{}}
	d.name = w.pickAlias(parent, long, hint, sn)
	d.tags = w.tagsFor(long, d.name, sn)
	d.path83 = len(d.name.Bytes())
	if parent != w.root {
		d.path83 += parent.path83 + 1
	}
	if d.path83 > maxDOSDir {
		w.deep = true
	}
	parent.children = append(parent.children, d)
	parent.byLong[long] = d
	parent.byAlias[d.name] = d
	return d, nil
}

func (w *Writer) addGroup(parent *wdir, long, hint string, sn *storedName) (*wgroup, error) {
	// Revisions copied from one archive share an alias; unrelated files that
	// happen to share an alias in different archives must not merge.
	if sn != nil {
		if g, ok := parent.byAlias[sn.name].(*wgroup); ok && strings.EqualFold(g.long, long) {
			return g, nil
		}
	}
	switch n := parent.byLong[long].(type) {
	case *wgroup:
		return n, nil
	case *wdir:
		return nil, fmt.Errorf("%w: %q is a directory", errBadName, long)
	}
	if len(parent.byAlias) >= maxDirEntries {
		return nil, errDirFull
	}
	g := &wgroup{name: w.pickAlias(parent, long, hint, sn), long: long}
	parent.groups = append(parent.groups, g)
	parent.byLong[long] = g
	parent.byAlias[g.name] = g
	return g, nil
}

func (w *Writer) checkDepth(d *wdir, n format.Name) {
	l := len(n.Bytes())
	if d != w.root {
		l += d.path83 + 1
	}
	if l > maxDOSPath {
		w.deep = true
	}
}

// entryWriter receives the contents of a file.
type entryWriter struct {
	w                *Writer
	rev              *wrev
	name             format.Name
	buf              []byte
	delta            int
	frags            int
	prev             []byte // raw tail of the previous fragment
	stream           bool
	closed           bool
	seedOff, seedLen int
}

func (e *entryWriter) Write(p []byte) (int, error) {
	w := e.w
	if e.closed {
		return 0, errors.New("uc2: write to closed entry")
	}
	if w.err != nil {
		return 0, w.err
	}
	n := len(p)
	e.rev.size += int64(n)
	for len(p) > 0 && w.err == nil {
		// A full buffer is only submitted once more data arrives, so the last fragment is never empty.
		if len(e.buf) == fragSize {
			if !e.stream {
				e.startStream()
			}
			w.submitFragment(e, e.buf, false)
			e.buf = make([]byte, 0, fragSize)
		}
		k := min(fragSize-len(e.buf), len(p))
		e.buf = append(e.buf, p[:k]...)
		p = p[k:]
	}
	return n, w.err
}

// startStream switches an entry larger than smallMax to fragmented compression.
func (e *entryWriter) startStream() {
	w := e.w
	e.stream = true
	if w.cfg.level >= Tight {
		e.delta = ultra.Analyze(e.buf, int(w.cfg.level))
	}
	e.rev.master = w.zeroMaster()
	e.rev.method = deltaMethod(w.cfg.level, e.delta)
}

func (w *Writer) submitFragment(e *entryWriter, frag []byte, last bool) {
	j := &job{rev: e.rev, delta: e.delta, first: e.frags == 0, last: last, weight: 2*int64(len(frag)) + int64(len(e.prev))}
	j.in = ultra.Input{Data: frag, Level: int(w.cfg.level), First: j.first, Final: last}
	if j.first {
		j.dict = zeroDict
	} else {
		j.in.Hist = e.prev
	}
	e.prev = frag[max(0, len(frag)-(ultra.MaxDist+8)):]
	e.frags++
	w.submit(j)
}

func deltaMethod(l Level, delta int) uint16 {
	switch {
	case delta == 0:
		return uint16(l)
	case l == SuperTight:
		return uint16(39 + delta)
	}
	return uint16(29 + delta)
}

func (w *Writer) closeEntry() error {
	e := w.cur
	if e == nil {
		return w.err
	}
	w.cur, e.closed = nil, true
	if e.stream {
		w.submitFragment(e, e.buf, true)
		e.buf = nil
		return w.err
	}
	w.batch = append(w.batch, e)
	w.batchN += len(e.buf)
	if w.batchN >= batchMax {
		w.flushBatch()
	}
	return w.err
}

// SetComment sets the archive comment, stored as the root file U$~COMM.TXT.
func (w *Writer) SetComment(s string) error {
	w.comment, w.changed = &s, true
	return nil
}

// SetLabel sets the archive volume label (at most 11 characters).
func (w *Writer) SetLabel(s string) error {
	b, _ := encodeOEM(s, w.cfg.charset)
	if len(b) > 11 {
		return fmt.Errorf("uc2: label %q too long", s)
	}
	for i := range w.tail.Label {
		w.tail.Label[i] = ' '
	}
	copy(w.tail.Label[:], b)
	if s == "" {
		w.tail.Label = [11]byte{}
	}
	w.changed = true
	return nil
}

// Copy adds f from another archive, keeping its 8.3 name and tags. The
// compressed data is copied as is when the original UC2 can read it in the new
// archive; otherwise it is recompressed. The data may be read from f's archive
// until Close, which must stay open and unchanged until then.
func (w *Writer) Copy(f *File) error {
	if err := w.begin(); err != nil {
		return err
	}
	sn := f.stored
	if w.comment != nil && f.rec.Meta.Parent == 0 && f.rec.Meta.Name.String() == "U$~COMM.TXT" {
		return nil
	}
	fh := f.FileHeader
	if f.rec.Type == format.BoDir {
		_, err := w.create(&fh, sn)
		return err
	}
	if m := f.r.rawMaster(f); m != nil && !f.r.pcp {
		ew, err := w.create(&fh, sn)
		if err != nil {
			return err
		}
		w.cur = nil
		ew.closed = true
		rev := ew.rev
		rev.size, rev.fletch, rev.method = f.Size, f.rec.Fletch, f.rec.Comp.Method
		rev.meta.Attr, rev.meta.Date, rev.meta.Time = f.rec.Meta.Attr, f.rec.Meta.Date, f.rec.Meta.Time
		rev.master = w.copyMaster(f.r, f.rec.Comp.Prefix, m)
		w.submit(&job{src: f.r.ra, srcOff: f.offset, rev: rev, first: true, last: true, srcLen: f.CompressedSize})
		return w.err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	ew, err := w.create(&fh, sn)
	if err != nil {
		return err
	}
	if _, err := io.Copy(ew, rc); err != nil {
		return w.fail(err)
	}
	return w.closeEntry()
}

// rawMaster returns the master of f if f can be copied without recompression.
func (r *Reader) rawMaster(f *File) *format.Master {
	d, turbo, err := format.MethodInfo(f.rec.Comp.Method)
	if err != nil || turbo || d > 8 || f.rec.Comp.Prefix < format.FirstMaster {
		return nil
	}
	m := r.masters[f.rec.Comp.Prefix]
	if m == nil || m.Len%512 != 0 || m.Len < format.MinMaster || m.Len > format.MaxMaster ||
		!format.ValidMasterMethod(m.Comp.Method) {
		return nil
	}
	if p := m.Comp.Prefix; p != format.SuperMaster && p != format.NoMaster && p != format.LegacyPrefix {
		return nil
	}
	if _, err := r.master(m.Index); err != nil {
		return nil
	}
	return m
}

func (w *Writer) copyMaster(r *Reader, idx uint32, m *format.Master) *wmaster {
	k := copyKey{r.id, idx}
	if wm := w.copied[k]; wm != nil {
		return wm
	}
	prefix := m.Comp.Prefix
	if prefix == format.LegacyPrefix {
		prefix = format.SuperMaster
	}
	wm := &wmaster{index: w.nextMaster, key: m.Key, length: int(m.Len), method: m.Comp.Method, prefix: prefix}
	w.nextMaster++
	w.masters = append(w.masters, wm)
	w.copied[k] = wm
	off, _ := r.abs(m.Loc)
	w.submit(&job{src: r.ra, srcOff: off, srcLen: int64(m.Comp.CompLen), mas: wm, first: true, last: true})
	return wm
}

// AddFS adds the files of fsys in lexical order. Only regular files and
// directories are supported. Files are read ahead by up to WithConcurrency
// goroutines, so fsys must be safe for concurrent use.
func (w *Writer) AddFS(fsys fs.FS) error {
	type item struct {
		path  string
		info  fs.FileInfo
		f     fs.File // open large file
		data  []byte  // contents of a small file
		err   error
		ready chan struct{}
	}
	var items []*item
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == "." {
			return err
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return fmt.Errorf("uc2: cannot add non-regular file %q", p)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		os.SameFile(info, info) // loads the file ID on Windows now, not at comparison time
		items = append(items, &item{path: p, info: info, ready: make(chan struct{})})
		return nil
	})
	if err != nil {
		return err
	}
	sem := make(chan struct{}, w.cfg.concurrency)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	next := len(items)
	defer func() {
		close(stop)
		wg.Wait()
		for _, it := range items[next:] {
			if it.f != nil {
				it.f.Close()
			}
		}
	}()
	wg.Go(func() {
		for _, it := range items {
			select {
			case sem <- struct{}{}:
			case <-stop:
				return
			}
			wg.Go(func() {
				defer close(it.ready)
				if !it.info.IsDir() {
					it.f, it.data, it.err = openFSFile(fsys, it.path, it.info)
				}
			})
		}
	})
	for i, it := range items {
		<-it.ready
		next = i + 1
		err := w.addFSItem(it.path, it.info, it.f, it.data, it.err)
		it.data = nil
		<-sem
		if err != nil {
			return err
		}
	}
	return w.closeEntry()
}

// openFSFile opens a file found by the walk, checking it is still the same
// regular file: it may have been replaced by a link, FIFO or device since.
// Small files are read completely, larger ones are returned open.
func openFSFile(fsys fs.FS, p string, want fs.FileInfo) (fs.File, []byte, error) {
	changed := fmt.Errorf("uc2: %q changed during archiving", p)
	// Opening a FIFO blocks, so check before opening as well.
	if fi, err := fs.Stat(fsys, p); err != nil || !fi.Mode().IsRegular() || !sameFile(fi, want) {
		return nil, nil, changed
	}
	f, err := fsys.Open(p)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !sameFile(fi, want) {
		f.Close()
		return nil, nil, changed
	}
	if fi.Size() > smallMax {
		return f, nil, nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, fi.Size()+1))
	if err == nil && int64(len(data)) != fi.Size() {
		err = changed
	}
	return nil, data, err
}

// sameFile compares file identity when the file system provides it.
func sameFile(a, b fs.FileInfo) bool {
	if os.SameFile(a, a) {
		return os.SameFile(a, b)
	}
	return a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (w *Writer) addFSItem(p string, info fs.FileInfo, f fs.File, data []byte, err error) error {
	if f != nil {
		defer f.Close()
	}
	if err != nil {
		return err
	}
	fh, err := FileInfoHeader(info)
	if err != nil {
		return err
	}
	fh.Name = p
	if info.IsDir() {
		fh.Name += "/"
	}
	ew, err := w.CreateHeader(fh)
	if err != nil || info.IsDir() {
		return err
	}
	if f == nil {
		_, err = ew.Write(data)
		return err
	}
	n, err := io.Copy(ew, io.LimitReader(f, info.Size()))
	if err == nil && n != info.Size() {
		err = fmt.Errorf("uc2: %q changed during archiving", p)
	}
	if err != nil {
		return w.fail(err)
	}
	return nil
}

// Close finishes the archive. It does not close the underlying writer.
func (w *Writer) Close() error {
	if w.closed {
		return w.err
	}
	if w.app != nil && w.app.unlock != nil {
		defer w.app.unlock()
	}
	if w.app != nil && !w.changed && w.protectOnly() {
		w.closed = true
		return w.finishProtect()
	}
	if w.begin() == nil {
		w.closeEntry()
		if w.comment != nil {
			w.addComment()
		}
		w.flushBatch()
	}
	w.drain(true, true)
	w.closed = true
	if w.err == nil {
		w.finish()
	}
	if w.err != nil && w.app != nil && !w.committed {
		// Remove the appended bytes; the original archive is unchanged.
		w.app.f.Truncate(w.app.size)
	}
	return w.err
}

// addComment stores the comment as the root file U$~COMM.TXT, replacing an
// earlier one. It is kept apart from user files of the same long name.
func (w *Writer) addComment() {
	w.dropComment()
	b, _ := encodeOEM(*w.comment, w.cfg.charset)
	name, _ := format.MakeName([]byte("U$~COMM"), []byte("TXT"))
	date, tm := format.DOSTime(time.Now())
	rev := &wrev{meta: format.Meta{Attr: uint8(AttrArchive), Date: date, Time: tm}}
	w.root.groups = append(w.root.groups, &wgroup{name: name, revs: []*wrev{rev}})
	w.root.byAlias[name] = w.root.groups[len(w.root.groups)-1]
	w.cur = &entryWriter{w: w, rev: rev, name: name}
	w.cur.Write(b)
	w.closeEntry()
}

func (w *Writer) finish() error {
	c, raw, vmem, big := w.buildCDIR()
	if len(raw) > maxReadCDIR {
		return w.fail(fmt.Errorf("uc2: central directory too large (%d bytes, max %d)", len(raw), maxReadCDIR))
	}
	cdirOff := w.off
	stream := ultra.Compress(raw, zeroDict(), int(min(w.cfg.level, Tight)))
	w.write(format.Compress{CompLen: uint32(len(stream)), Method: uint16(min(w.cfg.level, Tight)), Prefix: format.NoMaster}.Append(nil))
	w.write(stream)
	compEnd := w.off
	end := compEnd + format.FHeadSize
	if w.dp != nil {
		end += dp.AreaSize(compEnd)
	}
	extended := big || w.deep || len(raw) > maxRawCDIR || vmem > maxVMEM || end > maxCompat
	if err := c.Validate(extended); err != nil {
		return w.fail(fmt.Errorf("uc2: internal error: %w", err))
	}
	fh := format.FHead{CompLen: uint32(compEnd - format.FHeadSize), Protected: w.dp != nil}
	xh := format.XHead{Cdir: format.LocOf(cdirOff), Fletch: format.Fletch(raw), MadeBy: format.MadeBy, Needed: format.Needed}
	for _, m := range c.Masters {
		if m.Comp.Prefix == format.NoMaster {
			xh.Needed = format.NeededPUC // copied from an archive made with UC2_PUC
		}
	}
	if extended {
		xh.Needed = format.NeededLarge
	}
	head := xh.Append(fh.Append(nil))
	if w.dp != nil {
		area := w.dp.Area(head)
		w.dp = nil
		w.write(area)
	}
	w.write(head[:format.FHeadSize])
	if w.err == nil {
		w.err = w.bw.Flush()
	}
	if w.err != nil {
		return w.err
	}
	return w.commit(head)
}

// commit writes the final header at the start of the archive. For in-place
// updates the data is synced first, making the header write the commit point.
func (w *Writer) commit(head []byte) error {
	end := w.start + w.off
	if w.app != nil {
		if err := w.app.f.Sync(); err != nil {
			return w.fail(err)
		}
	}
	if _, err := w.w.Seek(w.start, io.SeekStart); err != nil {
		return w.fail(err)
	}
	if _, err := w.w.Write(head); err != nil {
		return w.fail(err)
	}
	if pos, err := w.w.Seek(0, io.SeekCurrent); err != nil || pos != w.start+int64(len(head)) {
		return w.fail(errors.New("uc2: output cannot be overwritten (opened for appending?)"))
	}
	w.committed = true
	if w.app != nil {
		if err := w.app.f.Sync(); err != nil {
			return w.fail(err)
		}
	}
	_, err := w.w.Seek(end, io.SeekStart)
	return w.fail(err)
}

// buildCDIR assembles the central directory in UC2's canonical order and
// estimates the memory UC2 needs to read it.
// big reports whether any size or offset exceeds what DOS UC2 handles.
func (w *Writer) buildCDIR() (c *format.CDIR, raw []byte, vmem int, big bool) {
	c = &format.CDIR{Tail: w.tail}
	next := uint32(1)
	var emit func(d *wdir)
	emit = func(d *wdir) {
		for _, ch := range d.children {
			ch.index = next
			next++
			m := ch.meta
			m.Parent, m.Name = d.index, ch.name
			c.Entries = append(c.Entries, format.Entry{Type: format.BoDir, Meta: m, Index: ch.index, Tags: ch.tags})
		}
		for _, g := range d.groups {
			vmem += 18
			for _, r := range g.revs {
				m := r.meta
				m.Parent, m.Name = d.index, g.name
				e := format.Entry{
					Type: format.BoFile, Meta: m, Size: uint32(r.size), Fletch: r.fletch,
					Comp: format.Compress{CompLen: uint32(r.comp), Method: r.method, Prefix: r.master.index},
					Loc:  format.LocOf(r.off), Tags: r.tags,
				}
				if r.size >= 1<<32 || r.comp >= 1<<32 {
					var b [16]byte
					binary.LittleEndian.PutUint64(b[:], uint64(r.size))
					binary.LittleEndian.PutUint64(b[8:], uint64(r.comp))
					e.Tags = append(append([]format.Tag(nil), e.Tags...), format.Tag{Name: tagSize64, Data: b[:]})
				}
				for _, t := range e.Tags {
					vmem += 43 + len(t.Data)
				}
				big = big || r.size > maxCompat || r.off+r.comp > maxCompat
				vmem += 100
				r.master.refCtr++
				r.master.refLen += uint32(r.size)
				c.Entries = append(c.Entries, e)
			}
		}
		for _, ch := range d.children {
			emit(ch)
		}
	}
	emit(w.root)
	for _, m := range w.masters {
		if m.refCtr == 0 {
			continue
		}
		big = big || m.off+m.comp > maxCompat
		c.Masters = append(c.Masters, format.Master{
			Index: m.index, Key: m.key, RefLen: m.refLen, RefCtr: m.refCtr, Len: uint16(m.length),
			Comp: format.Compress{CompLen: uint32(m.comp), Method: m.method, Prefix: m.prefix},
			Loc:  format.LocOf(m.off),
		})
	}
	raw = c.Append(nil)
	return c, raw, vmem + len(raw), big
}
