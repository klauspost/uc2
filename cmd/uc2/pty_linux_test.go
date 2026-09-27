package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/klauspost/uc2"
)

// openPTY returns the master and slave side of a new pseudo terminal of
// w x h characters, and a function that sets its size.
func openPTY(t *testing.T, w, h int) (master, slave *os.File, setSize func(w, h int)) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skip(err)
	}
	// Fd() would make m blocking, and Close could then not end a Read.
	rc, _ := m.SyscallConn()
	ioctl := func(req uintptr, p unsafe.Pointer) {
		var e syscall.Errno
		rc.Control(func(fd uintptr) { _, _, e = syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(p)) })
		if e != 0 {
			t.Fatal(e)
		}
	}
	var unlock int32
	var n uint32
	ioctl(syscall.TIOCSPTLCK, unsafe.Pointer(&unlock))
	ioctl(syscall.TIOCGPTN, unsafe.Pointer(&n))
	setSize = func(w, h int) {
		ws := [4]uint16{uint16(h), uint16(w)}
		ioctl(syscall.TIOCSWINSZ, unsafe.Pointer(&ws))
	}
	setSize(w, h)
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The master first: the help's key reader keeps a Read pending on the
	// slave, which ends only when the master is closed.
	t.Cleanup(func() { m.Close(); s.Close() })
	return m, s, setSize
}

// watchPTY collects the output of the terminal with master m. waitFor
// waits until s was written n times.
func watchPTY(t *testing.T, m *os.File) (waitFor func(s string, n int), output func() string) {
	var mu sync.Mutex
	var out []byte
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := m.Read(b)
			mu.Lock()
			out = append(out, b[:n]...)
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	output = func() string {
		mu.Lock()
		defer mu.Unlock()
		return string(out)
	}
	waitFor = func(s string, n int) {
		t.Helper()
		for start := time.Now(); strings.Count(output(), s) < n; time.Sleep(10 * time.Millisecond) {
			if time.Since(start) > 10*time.Second {
				t.Fatalf("no %q in %q", s, output())
			}
		}
	}
	return waitFor, output
}

// TestPTY runs the help on a real terminal: raw mode, size, keys, the
// resize signal and the restored terminal.
func TestPTY(t *testing.T) {
	m, s, setSize := openPTY(t, 100, 30)
	waitFor, output := watchPTY(t, m)
	t.Setenv("TERM", "xterm")
	a := newApp(s, s, new(bytes.Buffer))
	a.inTTY, a.termOut = true, true
	done := make(chan int)
	go func() {
		done <- a.helpCmd(nil)
		a.out.Flush()
		close(done)
	}()
	waitFor("Mini help: ", 1)
	m.Write([]byte("\x1b[B5"))
	waitFor("(BBS) 5. UC2 BBS COMMANDS", 1)
	setSize(120, 40)
	syscall.Kill(os.Getpid(), syscall.SIGWINCH)
	waitFor("\x1b[40;1H", 1)
	m.Write([]byte("\x1b")) // a lone Esc
	waitFor("Mini help: ", 2)
	m.Write([]byte("\x1b[A\x1b"))
	if code := <-done; code != 0 {
		t.Errorf("exit %d", code)
	}
	<-done
	waitFor("Everything went OK", 1)
	var tio syscall.Termios
	syscall.Syscall(syscall.SYS_IOCTL, s.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&tio)))
	if tio.Lflag&syscall.ICANON == 0 || tio.Lflag&syscall.ECHO == 0 {
		t.Error("terminal not restored")
	}
	o := output()
	// The menu came back with the highlight moved, and the terminal was
	// restored before the last words.
	if !strings.Contains(o, "\x1b[40;1H\x1b[0;0m"+strings.Repeat(" ", 120)) || !strings.Contains(o, "[Esc]\x1b[0m\r\n\x1b[0m"+wrapOn+curOn+"\r\n") {
		t.Errorf("output %q", o)
	}
}

// cooked reports whether the terminal f is in canonical mode.
func cooked(f *os.File) bool {
	var tio syscall.Termios
	syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&tio)))
	return tio.Lflag&syscall.ICANON != 0
}

// ptyRun runs uc2 with args on a new terminal, after prep changed the app.
// It returns the master and functions that wait for output and the exit.
func ptyRun(t *testing.T, prep func(a *app, tty *os.File), args ...string) (m *os.File, waitFor func(s string, n int), exit func() int) {
	m, s, _ := openPTY(t, 80, 25)
	waitFor, _ = watchPTY(t, m)
	t.Setenv("TERM", "xterm")
	a := newApp(s, s, new(bytes.Buffer))
	a.inTTY, a.termOut = true, true
	prep(a, s)
	done := make(chan int, 1)
	go func() { done <- a.run(args) }()
	return m, waitFor, func() int {
		select {
		case code := <-done:
			return code
		case <-time.After(10 * time.Second):
			t.Fatal("input lost")
			return 0
		}
	}
}

// TestPTYChained runs commands after viewers: all input must reach them,
// none may stay with the key reader of an earlier viewer.
func TestPTYChained(t *testing.T) {
	setup(t, map[string]string{"A.TXT": "first\n", "B.TXT": "second\n"})
	writeArchive(t, "C.UC2", "x")
	var tty *os.File
	m, waitFor, exit := ptyRun(t, func(_ *app, s *os.File) { tty = s }, "~V", "A.TXT", "&", "~V", "B.TXT", "&", "R", "C.UC2", "--comment-file=-")
	waitFor(" A.TXT ", 1)
	m.Write([]byte("\x1b"))
	waitFor(" B.TXT ", 1)
	m.Write([]byte("\x1b"))
	// Ctrl-D ends the input only after the viewer restored the terminal.
	for start := time.Now(); !cooked(tty); time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 10*time.Second {
			t.Fatal("terminal not restored")
		}
	}
	m.Write([]byte("line one\nline two\n\x04"))
	if code := exit(); code != 0 {
		t.Errorf("exit %d", code)
	}
	r, err := uc2.OpenReader("C.UC2")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if c, err := r.Comment(); strings.ReplaceAll(c, "\r", "") != "line one\nline two\n" || err != nil {
		t.Errorf("comment %q, %v", c, err)
	}

	// A key prompt on the console.
	os.WriteFile("x", []byte("old"), 0o666)
	m, waitFor, exit = ptyRun(t, func(a *app, tty *os.File) { a.key = a.consoleKey(tty) }, "~V", "A.TXT", "&", "E", "C.UC2")
	waitFor(" A.TXT ", 1)
	m.Write([]byte("\x1b"))
	waitFor("\x1b[0m"+wrapOn+curOn, 1)
	m.Write([]byte("y"))
	if code := exit(); code != 0 {
		t.Errorf("exit %d", code)
	}
	if b, _ := os.ReadFile("x"); string(b) == "old" {
		t.Error("x not overwritten")
	}
}
