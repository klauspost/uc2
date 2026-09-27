//go:build !windows

package uc2

import "io/fs"

func sysAttr(fi fs.FileInfo) Attr {
	if fi.IsDir() {
		return 0
	}
	return AttrArchive
}
