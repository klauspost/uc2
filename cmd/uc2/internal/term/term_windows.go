// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package term

import (
	"syscall"
	"unsafe"
)

type state struct {
	mode uint32
}

const (
	enableProcessedInput       = 0x1
	enableLineInput            = 0x2
	enableEchoInput            = 0x4
	enableVirtualTerminalInput = 0x200
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

func setConsoleMode(h syscall.Handle, mode uint32) error {
	if r, _, err := procSetConsoleMode.Call(uintptr(h), uintptr(mode)); r == 0 {
		return err
	}
	return nil
}

func isTerminal(fd int) bool {
	var st uint32
	err := syscall.GetConsoleMode(syscall.Handle(fd), &st)
	return err == nil
}

// This is intended to be used on a console input handle.
// See https://learn.microsoft.com/en-us/windows/console/setconsolemode
func makeRaw(fd int) (*State, error) {
	var st uint32
	if err := syscall.GetConsoleMode(syscall.Handle(fd), &st); err != nil {
		return nil, err
	}
	raw := st &^ (enableEchoInput | enableProcessedInput | enableLineInput)
	raw |= enableVirtualTerminalInput
	if err := setConsoleMode(syscall.Handle(fd), raw); err != nil {
		return nil, err
	}
	return &State{state{st}}, nil
}

func restore(fd int, state *State) error {
	return setConsoleMode(syscall.Handle(fd), state.mode)
}

// consoleScreenBufferInfo is CONSOLE_SCREEN_BUFFER_INFO, which package
// syscall lacks.
type consoleScreenBufferInfo struct {
	size, cursorPosition struct{ x, y int16 }
	attributes           uint16
	window               struct{ left, top, right, bottom int16 }
	maximumWindowSize    struct{ x, y int16 }
}

// getSize needs a console output handle.
func getSize(fd int) (width, height int, err error) {
	var info consoleScreenBufferInfo
	if r, _, e := procGetConsoleScreenBufferInfo.Call(uintptr(fd), uintptr(unsafe.Pointer(&info))); r == 0 {
		return 0, 0, e
	}
	return int(info.window.right-info.window.left) + 1, int(info.window.bottom-info.window.top) + 1, nil
}
