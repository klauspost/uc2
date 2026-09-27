package main

import (
	"io/fs"
	"os"
	"syscall"
	"unsafe"
)

var abortSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

const oNonblock = 0

// keepOwner is not needed: replaceFile keeps the security descriptor.
func keepOwner(*os.File, fs.FileInfo) error { return nil }

var procReplaceFileW = syscall.NewLazyDLL("kernel32.dll").NewProc("ReplaceFileW")

// replaceFile moves tmp over the existing file dst. Unlike a rename,
// ReplaceFileW keeps the ACL, owner and attributes of dst.
func replaceFile(tmp, dst string) error {
	// With a backup name, dst is never left under an unknown name when
	// the replacement cannot be moved in.
	bak := tmp + "~"
	var p [3]*uint16
	for i, s := range []string{dst, tmp, bak} {
		var err error
		if p[i], err = syscall.UTF16PtrFromString(s); err != nil {
			return err
		}
	}
	const ignoreMergeErrors = 2
	r, _, err := procReplaceFileW.Call(uintptr(unsafe.Pointer(p[0])), uintptr(unsafe.Pointer(p[1])), uintptr(unsafe.Pointer(p[2])), ignoreMergeErrors, 0, 0)
	if r != 0 {
		os.Remove(bak)
		return nil
	}
	switch err {
	case syscall.Errno(1176): // ERROR_UNABLE_TO_MOVE_REPLACEMENT: dst is at bak
		os.Rename(bak, dst)
	case syscall.Errno(1), syscall.Errno(50), syscall.Errno(87), syscall.Errno(120):
		// Invalid function, not supported, invalid parameter, not
		// implemented: file systems without ReplaceFileW support.
		return os.Rename(tmp, dst)
	}
	return &os.LinkError{Op: "replace", Old: tmp, New: dst, Err: err}
}

var procSetConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")

// enableVT turns on ANSI sequences for the console f and returns a function
// that restores the previous mode.
func enableVT(f *os.File) (restore func(), ok bool) {
	h := syscall.Handle(f.Fd())
	var mode uint32
	if syscall.GetConsoleMode(h, &mode) != nil {
		return nil, false
	}
	const vt = 0x0004 // ENABLE_VIRTUAL_TERMINAL_PROCESSING
	if mode&vt != 0 {
		return func() {}, true
	}
	if r, _, _ := procSetConsoleMode.Call(uintptr(h), uintptr(mode|vt)); r == 0 {
		return nil, false // a console without VT support, before Windows 10
	}
	return func() { procSetConsoleMode.Call(uintptr(h), uintptr(mode)) }, true
}

// openConsole opens the console for reading key presses.
func openConsole() (*os.File, error) { return os.OpenFile("CONIN$", os.O_RDWR, 0) }
