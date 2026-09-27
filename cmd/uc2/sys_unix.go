//go:build unix

package main

import (
	"io/fs"
	"os"
	"syscall"
)

var abortSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// oNonblock keeps opening a file swapped for a FIFO from blocking.
const oNonblock = syscall.O_NONBLOCK

// keepOwner gives f the owner and group of orig, if they differ.
func keepOwner(f *os.File, orig fs.FileInfo) error {
	o, ok1 := orig.Sys().(*syscall.Stat_t)
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	n, ok2 := fi.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 || o.Uid == n.Uid && o.Gid == n.Gid {
		return nil
	}
	return f.Chown(int(o.Uid), int(o.Gid))
}

func replaceFile(tmp, dst string) error { return os.Rename(tmp, dst) }
