package uc2

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

// The locked byte lies far beyond the end of the file, so readers are not blocked.
func lockRange() *syscall.Overlapped {
	return &syscall.Overlapped{Offset: 0xFFFFFFFE, OffsetHigh: 0x7FFFFFFF}
}

func lockFile(f *os.File) error {
	const exclusiveFailImmediately = 2 | 1
	r, _, err := procLockFileEx.Call(f.Fd(), exclusiveFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(lockRange())))
	if r == 0 {
		return err
	}
	return nil
}

func unlockFile(f *os.File) {
	procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(lockRange())))
}
