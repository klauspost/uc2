// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package term

import (
	"syscall"
	"unsafe"
)

type state struct {
	termios syscall.Termios
}

func ioctl(fd int, req uintptr, t *syscall.Termios) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(unsafe.Pointer(t))); e != 0 {
		return e
	}
	return nil
}

func isTerminal(fd int) bool {
	var t syscall.Termios
	return ioctl(fd, ioctlReadTermios, &t) == nil
}

func makeRaw(fd int) (*State, error) {
	var termios syscall.Termios
	if err := ioctl(fd, ioctlReadTermios, &termios); err != nil {
		return nil, err
	}

	oldState := State{state{termios: termios}}

	// This attempts to replicate the behaviour documented for cfmakeraw in
	// the termios(3) manpage.
	termios.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	termios.Oflag &^= syscall.OPOST
	termios.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	termios.Cflag &^= syscall.CSIZE | syscall.PARENB
	termios.Cflag |= syscall.CS8
	termios.Cc[syscall.VMIN] = 1
	termios.Cc[syscall.VTIME] = 0
	if err := ioctl(fd, ioctlWriteTermios, &termios); err != nil {
		return nil, err
	}

	return &oldState, nil
}

func restore(fd int, state *State) error {
	return ioctl(fd, ioctlWriteTermios, &state.termios)
}

// winsize is struct winsize of <sys/ioctl.h>, which package syscall lacks.
type winsize struct {
	row, col, xpixel, ypixel uint16
}

func getSize(fd int) (width, height int, err error) {
	var ws winsize
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); e != 0 {
		return 0, 0, e
	}
	return int(ws.col), int(ws.row), nil
}
