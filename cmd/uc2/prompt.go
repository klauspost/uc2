package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/klauspost/uc2/cmd/uc2/internal/term"
)

// option is a menu choice; key is the highlighted hot key between pre and post.
type option struct{ pre, key, post string }

// canAsk reports whether questions can be answered: by key presses on the
// console, or by lines on standard input.
func (a *app) canAsk() bool {
	if a.console && a.key == nil {
		a.console = false
		if f, err := openConsole(); err == nil {
			if term.IsTerminal(int(f.Fd())) {
				a.key = a.consoleKey(f)
				a.restore = append(a.restore, func() { f.Close() })
			} else {
				f.Close()
			}
		}
	}
	return a.key != nil || a.tty
}

// consoleKey reads single key presses from the console f in raw mode.
func (a *app) consoleKey(f *os.File) func() ([]byte, error) {
	return func() ([]byte, error) {
		fd := int(f.Fd())
		st, err := term.MakeRaw(fd)
		if err != nil {
			return nil, err
		}
		// An interrupt must not leave the terminal in raw mode.
		setUnraw := func(fn func()) {
			tempMu.Lock()
			a.unraw = fn
			tempMu.Unlock()
		}
		setUnraw(func() { term.Restore(fd, st) })
		defer func() {
			setUnraw(nil)
			term.Restore(fd, st)
		}()
		r := io.Reader(f)
		if a.keys != nil {
			r = a.keys // it already waits for the next key on stdin, the same terminal
		}
		b := make([]byte, 16)
		n, err := r.Read(b)
		return b[:n], err
	}
}

// ask shows a UC2 menu (MENU.CPP Choice) on stderr and returns the chosen
// option, or -1 at the end of the input.
func (a *app) ask(question string, opts []option) (int, error) {
	a.breakLine()
	fmt.Fprintf(a.errOut, "\n"+cAsk+"%s"+cN+"\n", clean(question))
	for i, o := range opts {
		fmt.Fprintf(a.errOut, cAsk+"   %d "+cN+"-> %s"+cAsk+"%s"+cN+"%s\n", i+1, o.pre, o.key, o.post)
	}
	for {
		fmt.Fprint(a.errOut, cAsk+"CHOICE "+cN+"("+cAsk+"+"+cN+"=Abort) "+cAsk+"? ")
		var k string
		if a.key != nil {
			b, err := a.key()
			if err != nil || len(b) == 0 {
				fmt.Fprintln(a.errOut)
				return -1, nil
			}
			k = a.echo(b)
		} else {
			line, err := a.in.ReadString('\n')
			if k = strings.ToUpper(strings.TrimSpace(line)); err != nil && k == "" {
				fmt.Fprintln(a.errOut)
				return -1, nil
			}
		}
		if k == "+" || k == "\x03" {
			return 0, fatalf(sevAbort, "program aborted by user")
		}
		for i, o := range opts {
			if k == strings.ToUpper(o.key) || k == string(rune('1'+i)) {
				if a.key != nil {
					fmt.Fprint(a.errOut, "\n"+cN)
				}
				return i, nil
			}
		}
		if a.key != nil {
			fmt.Fprint(a.errOut, cErr+" *** WRONG CHOICE ***\r")
			a.sleep(500 * time.Millisecond)
			fmt.Fprint(a.errOut, strings.Repeat(" ", 57)+"\r")
		}
	}
}

// echo shows a key press like UC2 and returns it, upper case.
func (a *app) echo(b []byte) string {
	var shown, key string
	switch {
	case b[0] == '\r' || b[0] == '\n':
		shown, key = "[Enter]", "\r"
	case b[0] == 0x1b && len(b) == 1:
		shown, key = "[Esc]", "\x1b"
	case b[0] < 0x20 || b[0] == 0x7f:
		// Control and extended keys; the latter arrive as escape sequences.
		shown, key = a.deco("■"), string(b[:1])
	default:
		r, _ := utf8.DecodeRune(b)
		key = strings.ToUpper(string(r))
		shown = key
	}
	fmt.Fprint(a.errOut, clean(shown))
	return key
}
