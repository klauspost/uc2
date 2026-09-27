package term

import (
	"io"
	"strconv"
	"testing"
	"time"
)

func (k Key) String() string {
	if k >= KeyResize && k <= KeyNone {
		return [...]string{"None", "Up", "Down", "Left", "Right", "PgUp", "PgDn", "Home", "End", "Other", "Resize"}[-k]
	}
	return strconv.QuoteRune(rune(k))
}

func TestParseKey(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Key
		n    int
	}{
		{"\x1b[A", KeyUp, 3}, {"\x1bOA", KeyUp, 3}, {"\x1b[B", KeyDown, 3}, {"\x1bOB", KeyDown, 3},
		{"\x1b[C", KeyRight, 3}, {"\x1bOC", KeyRight, 3}, {"\x1b[D", KeyLeft, 3}, {"\x1bOD", KeyLeft, 3},
		{"\x1b[H", KeyHome, 3}, {"\x1bOH", KeyHome, 3}, {"\x1b[1~", KeyHome, 4}, {"\x1b[7~", KeyHome, 4},
		{"\x1b[F", KeyEnd, 3}, {"\x1bOF", KeyEnd, 3}, {"\x1b[4~", KeyEnd, 4}, {"\x1b[8~", KeyEnd, 4},
		{"\x1b[5~", KeyPgUp, 4}, {"\x1b[6~", KeyPgDn, 4}, {"\x1b[5;5~", KeyPgUp, 6},
		// Modifiers: Windows sends ESC[1;5A for Ctrl+Up.
		{"\x1b[1;5A", KeyUp, 6}, {"\x1b[1;2F", KeyEnd, 6},
		// Insert, Delete, F-keys, paste markers: no meaning, whole sequence consumed.
		{"\x1b[2~", KeyOther, 4}, {"\x1b[3~", KeyOther, 4}, {"\x1bOP", KeyOther, 3}, {"\x1b[15~", KeyOther, 5},
		{"\x1b[200~x", KeyOther, 6}, {"\x1b[1~5", KeyHome, 4},
		// Linux console F1-F5 must not become '[' and a paragraph letter.
		{"\x1b[[A", KeyOther, 4},
		// Alt+q, and an Esc followed by a control character.
		{"\x1bq", KeyEsc, 1}, {"\x1b[\x1b[A", KeyEsc, 1},
		{"\r", KeyEnter, 1}, {"\n", KeyEnter, 1}, {"\t", KeyTab, 1},
		{"\x7f", KeyBack, 1}, {"\x08", KeyBack, 1}, {"\x03", KeyCtrlC, 1},
		{"a", 'a', 1}, {"S", 'S', 1}, {" ", ' ', 1}, {"é", 'é', 2}, {"€x", '€', 3},
		// Incomplete keys.
		{"", KeyNone, 0}, {"\x1b", KeyNone, 0}, {"\x1b[", KeyNone, 0}, {"\x1b[1;5", KeyNone, 0},
		{"\x1bO", KeyNone, 0}, {"\x1b[[", KeyNone, 0}, {"\xc3", KeyNone, 0}, {"\xe2\x82", KeyNone, 0},
	} {
		if k, n := ParseKey([]byte(tc.in)); k != tc.want || n != tc.n {
			t.Errorf("%q: %v, %d bytes; want %v, %d", tc.in, k, n, tc.want, tc.n)
		}
	}
}

// TestKeys needs no sleeps: the timer only runs out when nothing else can
// arrive, and a timer of an hour cannot beat input that is already queued.
func TestKeys(t *testing.T) {
	in := make(chan []byte, 4)
	resize := make(chan struct{}, 1)
	k := &Keys{in: in, Resize: resize}
	for _, step := range []struct {
		wait time.Duration
		send []string
		want []Key
	}{
		{time.Millisecond, []string{"\x1b"}, []Key{KeyEsc}}, // lone Esc
		{time.Hour, []string{"\x1b", "[", "5~"}, []Key{KeyPgUp}},
		{time.Hour, []string{"\x1b[A\x1b[6~q"}, []Key{KeyUp, KeyPgDn, 'q'}},
		{time.Millisecond, []string{"\x1b["}, []Key{KeyEsc, '['}}, // a sequence cut short
		{time.Millisecond, []string{"\xc3"}, []Key{0xfffd}},
		{time.Hour, []string{"\xc3", "\xa9"}, []Key{'é'}},
	} {
		k.wait = step.wait
		for _, s := range step.send {
			in <- []byte(s)
		}
		for _, want := range step.want {
			if got, err := k.Key(); got != want || err != nil {
				t.Errorf("%q: %v, %v; want %v", step.send, got, err, want)
			}
		}
	}

	// A resize while a sequence is incomplete keeps the sequence.
	k.wait = time.Hour
	in <- []byte("\x1b[")
	resize <- struct{}{}
	if got, _ := k.Key(); got != KeyResize {
		t.Errorf("resize: %v", got)
	}
	in <- []byte("B")
	if got, _ := k.Key(); got != KeyDown {
		t.Errorf("after resize: %v", got)
	}

	// Flush drops what has arrived, parsed or not.
	in <- []byte("\x1b[B\x1b[B")
	in <- []byte("x")
	if got, _ := k.Key(); got != KeyDown {
		t.Errorf("before flush: %v", got)
	}
	k.Flush()
	in <- []byte("y")
	if got, _ := k.Key(); got != 'y' {
		t.Errorf("after flush: %v", got)
	}

	// A pending Esc is still a key at the end of the input.
	in <- []byte("\x1b")
	close(in)
	for _, want := range []Key{KeyEsc, KeyNone, KeyNone} {
		if got, err := k.Key(); got != want || (want == KeyNone) != (err == io.EOF) {
			t.Errorf("at the end: %v, %v; want %v", got, err, want)
		}
	}
	k.Flush()
}

func TestNewKeys(t *testing.T) {
	r, w := io.Pipe()
	k := NewKeys(r)
	go func() {
		w.Write([]byte("\x1b[B"))
		w.Write([]byte("qline"))
		w.Write([]byte(" two\n"))
		w.Close()
	}()
	for _, want := range []Key{KeyDown, 'q'} {
		if got, err := k.Key(); got != want || err != nil {
			t.Errorf("%v, %v; want %v", got, err, want)
		}
	}
	// The input after the keys reads as it is.
	if b, err := io.ReadAll(k); string(b) != "line two\n" || err != nil {
		t.Errorf("read %q, %v", b, err)
	}
	if _, err := k.Key(); err != io.EOF {
		t.Errorf("at the end: %v", err)
	}
}
