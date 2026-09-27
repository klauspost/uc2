package uc2

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/klauspost/uc2/internal/dp"
	"github.com/klauspost/uc2/internal/format"
)

// appendFile is the part of *os.File used by in-place updates.
type appendFile interface {
	io.ReaderAt
	io.WriteSeeker
	Truncate(size int64) error
	Sync() error
}

type appendState struct {
	f      appendFile
	r      *Reader
	size   int64
	unlock func()
	nodes  map[*File]any // the *wdir or *wrev loaded from each entry of r
}

// NewAppendWriter opens the archive in f (which must be opened for reading and
// writing) for adding files in place, like UC2's incremental mode: existing
// data is kept and new revisions are appended. The archive is updated
// atomically when Close rewrites the header; until then it is unchanged apart
// from appended bytes. Closing without adding anything, with
// WithDamageProtection, adds or removes damage protection. Until Close, f is
// locked against other append writers.
func NewAppendWriter(f *os.File, opts ...Option) (*Writer, error) {
	if err := lockFile(f); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLocked, err)
	}
	fi, err := f.Stat()
	if err != nil {
		unlockFile(f)
		return nil, err
	}
	w, err := newAppendWriter(f, fi.Size(), opts)
	if err != nil {
		unlockFile(f)
		return nil, err
	}
	w.app.unlock = func() { unlockFile(f) }
	return w, nil
}

func newAppendWriter(f appendFile, size int64, opts []Option) (*Writer, error) {
	r, err := NewReader(f, size, opts...)
	if err != nil {
		return nil, err
	}
	if r.pcp {
		return nil, unsupported("archive uses a private compression profile")
	}
	if err := r.Check(); err != nil {
		return nil, err
	}
	w := NewWriter(f, opts...)
	w.app = &appendState{f: f, r: r, size: size, nodes: map[*File]any{}}
	if w.cfg.protect == nil {
		w.cfg.protect = &r.Protected
	}
	w.tail = r.cdir.Tail
	if err := w.load(r); err != nil {
		return nil, err
	}
	return w, nil
}

// load recreates the directory tree and masters of r, keeping all records.
func (w *Writer) load(r *Reader) error {
	masters := map[uint32]*wmaster{}
	for _, m := range r.cdir.Masters {
		prefix := m.Comp.Prefix
		if prefix == format.LegacyPrefix {
			prefix = format.SuperMaster
		}
		off, err := r.abs(m.Loc)
		if err != nil {
			return err
		}
		wm := &wmaster{index: m.Index, key: m.Key, length: int(m.Len), method: m.Comp.Method, prefix: prefix, off: off, comp: int64(m.Comp.CompLen)}
		masters[m.Index] = wm
		w.masters = append(w.masters, wm)
		w.nextMaster = max(w.nextMaster, m.Index+1)
	}
	dirs := map[uint32]*wdir{0: w.root}
	for _, f := range r.File {
		e := f.rec
		parent := dirs[e.Meta.Parent]
		long := path.Base(strings.TrimSuffix(f.Name, "/"))
		if e.Type == format.BoDir {
			d := &wdir{parent: parent, name: e.Meta.Name, tags: e.Tags, meta: e.Meta, byLong: map[string]any{}, byAlias: map[format.Name]any{}}
			w.app.nodes[f] = d
			d.path83 = len(d.name.Bytes())
			if parent != w.root {
				d.path83 += parent.path83 + 1
			}
			w.deep = w.deep || d.path83 > maxDOSDir
			parent.children = append(parent.children, d)
			if parent.byLong[long] == nil {
				parent.byLong[long] = d
			}
			parent.byAlias[d.name] = d
			if _, dup := dirs[e.Index]; !dup && e.Index != 0 {
				dirs[e.Index] = d
			}
			continue
		}
		w.checkDepth(parent, e.Meta.Name)
		g, _ := parent.byAlias[e.Meta.Name].(*wgroup)
		if g == nil {
			g = &wgroup{name: e.Meta.Name, long: long}
			parent.groups = append(parent.groups, g)
			parent.byAlias[g.name] = g
			if parent.byLong[long] == nil {
				parent.byLong[long] = g
			}
		}
		m := masters[e.Comp.Prefix]
		if m == nil {
			return unsupported("file without a custom master")
		}
		var tags []format.Tag
		for _, t := range e.Tags {
			if t.Name != tagSize64 {
				tags = append(tags, t)
			}
		}
		rev := &wrev{meta: e.Meta, tags: tags, size: f.Size, fletch: e.Fletch,
			method: e.Comp.Method, master: m, off: f.offset, comp: f.CompressedSize}
		g.revs = append(g.revs, rev)
		w.app.nodes[f] = rev
	}
	return nil
}

// replaceTags implements tags.Replace.
func (w *Writer) replaceTags(f *File, t []format.Tag) error {
	if w.app == nil || w.closed {
		return errors.New("uc2: not an open append writer")
	}
	var cur *[]format.Tag
	switch n := w.app.nodes[f].(type) {
	case *wrev:
		cur = &n.tags
	case *wdir:
		cur = &n.tags
	default:
		return errors.New("uc2: entry is not in the archive")
	}
	isName := func(x format.Tag) bool { return x.Name == tagLongName || x.Name == tagUTF8Name }
	isSize := func(x format.Tag) bool { return x.Name == tagSize64 }
	nt := slices.DeleteFunc(slices.Clone(*cur), func(x format.Tag) bool { return !isName(x) })
	for _, x := range t {
		switch {
		case x.Name == "" || len(x.Name) > format.TagNameMax || strings.IndexByte(x.Name, 0) >= 0 || len(x.Data) > format.MaxTagSize:
			return fmt.Errorf("uc2: invalid tag %q", x.Name)
		case !isName(x) && !isSize(x):
			nt = append(nt, format.Tag{Name: x.Name, Data: bytes.Clone(x.Data)})
		}
	}
	eq := func(a, b format.Tag) bool { return a.Name == b.Name && bytes.Equal(a.Data, b.Data) }
	noSize := func(l []format.Tag) []format.Tag { return slices.DeleteFunc(slices.Clone(l), isSize) }
	// The first test keeps entries whose name tags follow other tags as they
	// are when the tags come back unchanged.
	if slices.EqualFunc(noSize(t), noSize(*cur), eq) || slices.EqualFunc(nt, *cur, eq) {
		return nil
	}
	*cur, w.changed = nt, true
	return nil
}

func (w *Writer) dropComment() {
	name, _ := format.MakeName([]byte("U$~COMM"), []byte("TXT"))
	g, _ := w.root.byAlias[name].(*wgroup)
	if g == nil {
		return
	}
	delete(w.root.byAlias, name)
	for long, n := range w.root.byLong {
		if n == g {
			delete(w.root.byLong, long)
		}
	}
	for i, x := range w.root.groups {
		if x == g {
			w.root.groups = append(w.root.groups[:i], w.root.groups[i+1:]...)
			break
		}
	}
}

func (w *Writer) beginAppend() error {
	a := w.app
	if _, err := w.w.Seek(a.size, io.SeekStart); err != nil {
		return w.fail(err)
	}
	w.off = a.size
	w.bw = bufio.NewWriterSize(w.w, 1<<20)
	if *w.cfg.protect {
		w.dp = new(dp.Sum)
		if _, err := io.Copy(w.dp, io.NewSectionReader(a.f, 0, a.size)); err != nil {
			return w.fail(err)
		}
	}
	return nil
}

// protectOnly reports whether closing an unchanged append writer can just add
// or remove the protection area. Protecting an archive that would then need
// extended mode takes the full update path instead.
func (w *Writer) protectOnly() bool {
	a := w.app
	l := a.r.compEnd
	return !*w.cfg.protect || a.r.Protected || a.r.extended || l+dp.AreaSize(l)+format.FHeadSize <= maxCompat
}

// finishProtect adds or removes damage protection of an unchanged archive.
// Each step leaves an archive with one valid primary header.
func (w *Writer) finishProtect() error {
	a := w.app
	want := *w.cfg.protect
	if want == a.r.Protected {
		return nil
	}
	l := a.r.compEnd
	var head [format.FHeadSize]byte
	if _, err := a.f.ReadAt(head[:], 0); err != nil {
		return w.fail(err)
	}
	head[12] = 0
	if want {
		head[12] = 1
		var s dp.Sum
		if _, err := io.Copy(&s, io.NewSectionReader(a.f, 0, l)); err != nil {
			return w.fail(err)
		}
		restore := func(err error) error {
			// The area overwrote the old spare header; put it back.
			a.f.Truncate(l)
			old := head
			old[12] = 0
			writeAt(a.f, old[:], l)
			return w.fail(err)
		}
		b := append(s.Area(head[:]), head[:]...)
		if err := writeAt(a.f, b, l); err != nil {
			return restore(err)
		}
		if err := a.f.Truncate(l + int64(len(b))); err != nil {
			return restore(err)
		}
		if err := a.f.Sync(); err != nil {
			return restore(err)
		}
		if err := writeAt(a.f, head[:], 0); err != nil {
			return w.fail(err)
		}
		return w.fail(a.f.Sync())
	}
	if err := writeAt(a.f, head[:], 0); err != nil {
		return w.fail(err)
	}
	if err := a.f.Sync(); err != nil {
		return w.fail(err)
	}
	if err := a.f.Truncate(l); err != nil {
		return w.fail(err)
	}
	if err := writeAt(a.f, head[:], l); err != nil {
		return w.fail(err)
	}
	return w.fail(a.f.Sync())
}

func writeAt(f appendFile, b []byte, off int64) error {
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	_, err := f.Write(b)
	return err
}
