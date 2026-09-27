//go:build !unix && !windows

package main

import (
	"io/fs"
	"os"
)

var abortSignals = []os.Signal{os.Interrupt}

const oNonblock = 0

func keepOwner(*os.File, fs.FileInfo) error { return nil }

func replaceFile(tmp, dst string) error { return os.Rename(tmp, dst) }
