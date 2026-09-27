package uc2

import (
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/uc2/internal/format"
)

// fsIndex maps paths to the newest revisions and lists directory contents.
type fsIndex struct {
	files    map[string]*File
	children map[string][]fs.DirEntry
}

func (r *Reader) index() *fsIndex {
	r.fsOnce.Do(func() {
		newest := map[string]*File{}
		for _, f := range r.File {
			if f.Revision != 0 || isInternalName(f.rec.Meta.Name) {
				continue
			}
			p := strings.TrimSuffix(f.Name, "/")
			if old, ok := newest[p]; !ok || f.rec.Type == format.BoDir && old.rec.Type != format.BoDir {
				newest[p] = f
			}
		}
		// Every parent is a directory; a directory hides a file of the same name.
		dirs := map[string]bool{}
		for p, f := range newest {
			if f.rec.Type == format.BoDir {
				dirs[p] = true
			}
			for d := path.Dir(p); d != "." && !dirs[d]; d = path.Dir(d) {
				dirs[d] = true
			}
		}
		idx := &fsIndex{files: map[string]*File{}, children: map[string][]fs.DirEntry{".": nil}}
		for p, f := range newest {
			if f.rec.Type == format.BoFile && !dirs[p] {
				idx.files[p] = f
				idx.children[path.Dir(p)] = append(idx.children[path.Dir(p)], fs.FileInfoToDirEntry(f.FileInfo()))
			}
		}
		for p := range dirs {
			f := newest[p]
			if f == nil || f.rec.Type != format.BoDir {
				f = &File{FileHeader: FileHeader{Name: p + "/", Attr: AttrDir}, r: r, rec: &format.Entry{Type: format.BoDir}}
			}
			idx.files[p] = f
			if idx.children[p] == nil {
				idx.children[p] = []fs.DirEntry{}
			}
			idx.children[path.Dir(p)] = append(idx.children[path.Dir(p)], fs.FileInfoToDirEntry(f.FileInfo()))
		}
		for _, c := range idx.children {
			slices.SortFunc(c, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
		}
		r.fsIdx = idx
	})
	return r.fsIdx
}

// Open opens the named file using fs.FS semantics. Only the newest revision of
// each name is visible, and UC2 internal files (U$~*) are hidden.
func (r *Reader) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	idx := r.index()
	f, ok := idx.files[name]
	_, isDir := idx.children[name]
	switch {
	case name == ".":
		return &openDir{info: rootInfo{}, entries: idx.children["."]}, nil
	case ok && f.rec.Type == format.BoFile:
		rc, err := f.Open()
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		return &openFile{ReadCloser: rc, f: f}, nil
	case ok || isDir:
		info := fs.FileInfo(rootInfo{name: path.Base(name)})
		if ok {
			info = f.FileInfo()
		}
		return &openDir{info: info, entries: idx.children[name]}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

type openFile struct {
	io.ReadCloser
	f *File
}

func (o *openFile) Stat() (fs.FileInfo, error) { return o.f.FileInfo(), nil }

type openDir struct {
	info    fs.FileInfo
	entries []fs.DirEntry
	pos     int
}

func (d *openDir) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *openDir) Close() error               { return nil }
func (d *openDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.Name(), Err: fs.ErrInvalid}
}

func (d *openDir) ReadDir(n int) ([]fs.DirEntry, error) {
	rest := d.entries[d.pos:]
	if n <= 0 {
		d.pos = len(d.entries)
		return slices.Clone(rest), nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	rest = rest[:min(n, len(rest))]
	d.pos += len(rest)
	return slices.Clone(rest), nil
}

type rootInfo struct{ name string }

func (i rootInfo) Name() string {
	if i.name == "" {
		return "."
	}
	return i.name
}
func (rootInfo) Size() int64        { return 0 }
func (rootInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (rootInfo) ModTime() time.Time { return time.Time{} }
func (rootInfo) IsDir() bool        { return true }
func (rootInfo) Sys() any           { return nil }
