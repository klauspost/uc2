//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || windows)

package uc2

import "os"

func lockFile(*os.File) error { return nil }
func unlockFile(*os.File)     {}
