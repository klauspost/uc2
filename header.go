package uc2

import (
	"io/fs"
	"path"
	"strings"
	"time"
)

// FileHeader describes a file or directory in an archive.
type FileHeader struct {
	// Name is the slash-separated path. Directories end in "/".
	Name string

	// ShortName is the DOS 8.3 name of the last path element, for example
	// "README~1.TXT". When writing it is a hint that is used if valid and free.
	ShortName string

	// Modified is stored as DOS local time with 2 second resolution.
	Modified time.Time

	Attr Attr

	// Size is the uncompressed size. It is ignored when writing.
	Size int64

	// stored is the on-disk name and tags of a header read from an archive.
	// CreateHeader keeps them while Name is unchanged.
	stored     *storedName
	storedName string
}

func (h *FileHeader) isDir() bool { return strings.HasSuffix(h.Name, "/") }

// Mode returns a permission mode derived from the DOS attributes.
func (h *FileHeader) Mode() fs.FileMode {
	switch {
	case h.isDir():
		return fs.ModeDir | 0o755
	case h.Attr&AttrReadOnly != 0:
		return 0o444
	}
	return 0o644
}

// FileInfo returns an fs.FileInfo for the header.
func (h *FileHeader) FileInfo() fs.FileInfo { return headerInfo{h} }

type headerInfo struct{ h *FileHeader }

func (i headerInfo) Name() string       { return path.Base(strings.TrimSuffix(i.h.Name, "/")) }
func (i headerInfo) Size() int64        { return i.h.Size }
func (i headerInfo) IsDir() bool        { return i.h.isDir() }
func (i headerInfo) ModTime() time.Time { return i.h.Modified }
func (i headerInfo) Mode() fs.FileMode  { return i.h.Mode() }
func (i headerInfo) Sys() any           { return i.h }
func (i headerInfo) String() string     { return fs.FormatFileInfo(i) }

// FileInfoHeader creates a header from fi. The Name is the base name only;
// callers set the full path. Directories get a trailing slash.
func FileInfoHeader(fi fs.FileInfo) (*FileHeader, error) {
	h := &FileHeader{Name: fi.Name(), Modified: fi.ModTime(), Size: fi.Size()}
	if fh, ok := fi.Sys().(*FileHeader); ok {
		h.ShortName, h.Attr = fh.ShortName, fh.Attr
	} else {
		h.Attr = sysAttr(fi)
	}
	if fi.IsDir() {
		h.Name += "/"
		h.Attr |= AttrDir
		h.Size = 0
	} else if fi.Mode()&0o200 == 0 {
		h.Attr |= AttrReadOnly
	}
	return h, nil
}
