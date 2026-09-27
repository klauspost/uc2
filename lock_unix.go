//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package uc2

import (
	"os"
	"syscall"
)

func lockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
