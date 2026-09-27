package uc2

import (
	"io/fs"
	"syscall"
)

func sysAttr(fi fs.FileInfo) Attr {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return Attr(d.FileAttributes) & (AttrReadOnly | AttrHidden | AttrSystem | AttrArchive)
	}
	if fi.IsDir() {
		return 0
	}
	return AttrArchive
}
