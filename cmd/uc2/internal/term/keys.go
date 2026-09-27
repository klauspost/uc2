package term

import (
	"bytes"
	"io"
	"time"
	"unicode/utf8"
)

// Key is a key press: a character, or one of the keys below.
type Key rune

const (
	KeyNone Key = -iota // no complete key yet
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyPgUp
	KeyPgDn
	KeyHome
	KeyEnd
	KeyOther  // a sequence without a meaning here, such as F1
	KeyResize // the terminal changed its size
)

const (
	KeyCtrlC Key = 0x03
	KeyTab   Key = '\t'
	KeyEnter Key = '\r'
	KeyEsc   Key = 0x1b
	KeyBack  Key = 0x7f
)

// finalKeys maps the last byte of ESC [ ... and ESC O sequences.
var finalKeys = map[byte]Key{'A': KeyUp, 'B': KeyDown, 'C': KeyRight, 'D': KeyLeft, 'H': KeyHome, 'F': KeyEnd}

// tildeKeys maps n of ESC [ n ~ (VT220, Linux console, rxvt, Windows).
var tildeKeys = map[string]Key{"1": KeyHome, "4": KeyEnd, "5": KeyPgUp, "6": KeyPgDn, "7": KeyHome, "8": KeyEnd}

// ParseKey decodes the first key in b, as sent by terminals in raw mode,
// and returns its length, or 0 if b holds only the start of a key.
func ParseKey(b []byte) (Key, int) {
	if len(b) == 0 {
		return KeyNone, 0
	}
	switch c := b[0]; {
	case c == 0x1b:
		if len(b) == 1 {
			return KeyNone, 0
		}
		switch b[1] {
		case 'O': // SS3: application cursor mode
			if len(b) < 3 {
				return KeyNone, 0
			}
			if k, ok := finalKeys[b[2]]; ok {
				return k, 3
			}
			return KeyOther, 3
		case '[':
			if len(b) > 2 && b[2] == '[' { // Linux console F1-F5
				if len(b) < 4 {
					return KeyNone, 0
				}
				return KeyOther, 4
			}
			i := 2
			for i < len(b) && b[i] >= 0x20 && b[i] <= 0x3f {
				i++
			}
			switch {
			case i == len(b):
				return KeyNone, 0
			case b[i] < 0x40 || b[i] > 0x7e:
				return KeyEsc, 1 // not a sequence
			}
			// Modifiers (ESC [ 1 ; 5 A for Ctrl+Up) do not matter.
			n, _, _ := bytes.Cut(b[2:i], []byte(";"))
			if k, ok := finalKeys[b[i]]; ok {
				return k, i + 1
			}
			if k, ok := tildeKeys[string(n)]; ok && b[i] == '~' {
				return k, i + 1
			}
			return KeyOther, i + 1
		}
		return KeyEsc, 1 // Alt+x arrives as Esc and x
	case c == '\n':
		return KeyEnter, 1
	case c == 0x08:
		return KeyBack, 1
	case c < utf8.RuneSelf:
		return Key(c), 1
	}
	if !utf8.FullRune(b) {
		return KeyNone, 0
	}
	r, n := utf8.DecodeRune(b)
	return Key(r), n
}

// EscWait is how long the rest of an escape sequence may take to arrive
// after its Esc; an Esc followed by nothing is the Esc key.
const EscWait = 50 * time.Millisecond

// Keys reads key presses from a terminal in raw mode.
type Keys struct {
	Resize <-chan struct{} // each value is a KeyResize; may be nil
	in     <-chan []byte   // closed at the end of the input
	buf    []byte
	wait   time.Duration
}

// NewKeys returns the keys read from r. It reads r on its own goroutine,
// so that a timer can tell the Esc key from an escape sequence on every
// platform. The goroutine stays blocked in Read until the input ends, and
// takes the next input even after the last key was read: r must not be
// closed, and all later reads of r must go through the Keys.
func NewKeys(r io.Reader) *Keys {
	c := make(chan []byte)
	go func() {
		defer close(c)
		for {
			b := make([]byte, 256)
			n, err := r.Read(b)
			if n > 0 {
				c <- b[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	return &Keys{in: c, wait: EscWait}
}

// Read reads the input as it arrives, keys not read yet first.
func (k *Keys) Read(p []byte) (int, error) {
	if len(k.buf) == 0 {
		b, ok := <-k.in
		if !ok {
			return 0, io.EOF
		}
		k.buf = b
	}
	n := copy(p, k.buf)
	k.buf = k.buf[n:]
	return n, nil
}

// Key returns the next key press, or io.EOF at the end of the input.
func (k *Keys) Key() (Key, error) {
	for {
		if key, n := ParseKey(k.buf); n > 0 {
			k.buf = k.buf[n:]
			return key, nil
		}
		var timeout <-chan time.Time
		if len(k.buf) > 0 {
			timeout = time.After(k.wait)
		}
		select {
		case b, ok := <-k.in:
			if ok {
				k.buf = append(k.buf, b...)
				continue
			}
			if len(k.buf) == 0 {
				return KeyNone, io.EOF
			}
			return k.pop(), nil
		case <-timeout:
			return k.pop(), nil
		case <-k.Resize:
			return KeyResize, nil
		}
	}
}

// pop returns the first byte of an Esc, or of a sequence cut short, as a key.
func (k *Keys) pop() Key {
	c := Key(k.buf[0])
	k.buf = k.buf[1:]
	if c >= utf8.RuneSelf {
		return utf8.RuneError
	}
	return c
}

// Flush discards the input that has arrived but has not been read yet.
func (k *Keys) Flush() {
	k.buf = nil
	for {
		select {
		case _, ok := <-k.in:
			if !ok {
				return
			}
		default:
			return
		}
	}
}
