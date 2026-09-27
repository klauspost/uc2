package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/uc2/cmd/uc2/internal/term"
)

// UC2 switches colors with control bytes embedded in its format strings
// (VIDEO.CPP); the messages keep them and a painter translates them.
const (
	cT   = "\x03" // program title: light cyan
	cH   = "\x04" // headers: cyan
	cOK  = "\x05" // OK, DONE and good news: green
	cAsk = "\x06" // questions: yellow
	cN   = "\x07" // normal text
	cErr = "\x08" // errors and warnings: light red
)

// sgr holds the ANSI sequence of each color code. UC2 always paints on
// black; the terminal's own background and default color are kept instead,
// so that light themes stay readable.
var sgr = [10]string{3: "\x1b[96m", 4: "\x1b[36m", 5: "\x1b[32m", 6: "\x1b[93m", 7: "\x1b[0m", 8: "\x1b[91m"}

// Message levels, a bit mask as in UC2's Out(level, ...): a message is shown
// if its level has the bit of the verbosity.
const (
	lvVerbose = 1
	lvNormal  = 2 // normal only: the phase lines, which verbose output replaces by details
	lvStd     = lvVerbose | lvNormal
	lvQuiet   = 4
	lvAll     = 7
)

// painter translates the color codes to ANSI sequences, or drops them when
// the stream is not colored. As in UC2, a color stays in effect until the
// next code, also across writes; each write ends with a reset so that
// stdout and stderr never bleed into each other. It tracks the cursor
// column for progress bars.
type painter struct {
	w     io.Writer
	color bool
	cur   byte
	col   int
}

func (p *painter) Write(b []byte) (int, error) {
	out := make([]byte, 0, len(b)+16)
	if p.cur == 0 {
		p.cur = 7
	}
	shown := byte(7) // the color of the terminal, set lazily before text
	for _, c := range b {
		if c >= 1 && c <= 9 {
			if sgr[c] != "" {
				p.cur = c
			}
			continue
		}
		if p.color && c >= 0x20 && p.cur != shown {
			shown = p.cur
			out = append(out, sgr[shown]...)
		}
		p.advance(c)
		out = append(out, c)
	}
	if shown != 7 {
		out = append(out, sgr[7]...)
	}
	_, err := p.w.Write(out)
	return len(b), err
}

func (p *painter) advance(c byte) {
	switch {
	case c == '\n' || c == '\r':
		p.col = 0
	case c < 0x80 || c >= 0xc0: // not a UTF-8 continuation byte
		p.col++
	}
}

// raw writes s untranslated and flushes: progress bars move the cursor
// back with backspaces, which are color code 8 in messages.
func (p *painter) raw(s string) {
	for i := range len(s) {
		if s[i] == '\b' {
			p.col--
		} else {
			p.advance(s[i])
		}
	}
	io.WriteString(p.w, s)
	p.Flush()
}

func (p *painter) Flush() error {
	if f, ok := p.w.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// colorOn decides whether a stream is colored: --color always or never
// wins, otherwise only terminals are, unless NO_COLOR or TERM=dumb say no.
func colorOn(mode string, terminal bool) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	}
	return terminal && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

func isTerminal(x any) bool {
	f, ok := x.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// initConsole sets up colors for --color mode; on Windows it enables ANSI
// sequences for console streams until the program ends.
func (a *app) initConsole(mode string) {
	_, a.noHigh = os.LookupEnv("UC2_NO_HIGH_ASCII")
	for _, s := range []struct {
		p *painter
		w io.Writer
	}{{a.out, a.stdout}, {a.errOut, a.stderr}} {
		vt := false
		if isTerminal(s.w) {
			var restore func()
			if restore, vt = enableVT(s.w.(*os.File)); vt {
				a.restore = append(a.restore, restore)
			}
		}
		s.p.color = colorOn(mode, vt)
		if s.p == a.out {
			a.vtOut = vt
		}
	}
}

func (a *app) restoreConsole() {
	for _, r := range a.restore {
		r()
	}
	a.restore = nil
}

// deco applies UC2_NO_HIGH_ASCII to the logo and progress characters.
func (a *app) deco(s string) string {
	if !a.noHigh {
		return s
	}
	return strings.NewReplacer("·", "-", "■", "*", "═", "=", "░", "@", "█", "@").Replace(s)
}

var logoArt = []string{
	" ░███   ░███  ░████",
	"░█  ░█   ░█   ░█  ░█",
	"░█████   ░█   ░████",
	"░█  ░█   ░█   ░█",
	"░█  ░█  ░███  ░█",
}

// logoLines returns UC2's banner with color codes, with the title where
// UC2 had its own.
func (a *app) logoLines() []string {
	rule := cH + a.deco(strings.Repeat("═", 75))
	lines := []string{rule, cH + a.deco(logoArt[0]) + "      " + cT + "UltraCompressor II - Go - " + version()}
	for _, l := range logoArt[1:] {
		lines = append(lines, cH+a.deco(l))
	}
	return append(lines, rule)
}

func (a *app) logo() {
	if a.allowed(lvStd) {
		fmt.Fprint(a.out, strings.Join(a.logoLines(), "\n")+"\n\n")
	}
}

func (a *app) level() int {
	switch a.verbosity {
	case quiet:
		return lvQuiet
	case verbose:
		return lvVerbose
	}
	return lvNormal
}

func (a *app) allowed(l int) bool { return l&a.level() != 0 }

func (a *app) outl(l int, format string, args ...any) {
	if a.allowed(l) {
		fmt.Fprintf(a.out, format, cleanArgs(args)...)
	}
}

// endLine ends a line of level l, unless an error ended it already.
func (a *app) endLine(l int) {
	if a.allowed(l) && a.out.col > 0 {
		fmt.Fprint(a.out, "\n")
	}
}

// breakLine finishes a progress bar and the line on stdout before output
// to stderr, which shares the terminal.
func (a *app) breakLine() {
	a.endBar()
	if a.out.col > 0 {
		fmt.Fprint(a.out, "\n")
	}
	a.out.Flush()
}

// tick is a BIOS clock tick, at which UC2 redraws its progress bars.
const tick = 55 * time.Millisecond

// bar is UC2's progress indicator (VIDEO.CPP StartProgress): six dots after
// the message, filled with blocks while working and left full before DONE
// or OK. It is only drawn on a terminal.
type bar struct {
	mu    sync.Mutex
	a     *app
	total int64 // known size, or <= 0
	done  int64
	seg   int
	ticks int // redraws on the clock, for bars of unknown size
	spin  int
	pos   int // cursor position within the bar
	last  time.Time
	stop  func()
}

// startBar starts a bar for work of total bytes, or of unknown size if
// total <= 0, after a message of level l.
func (a *app) startBar(l int, total int64) {
	if !a.termOut || !a.allowed(l) || a.bar != nil {
		return
	}
	a.bar = &bar{a: a, total: total, last: a.now()}
	s := a.deco("······")
	if a.out.col > 68 {
		// UC2 keeps the bar on an 80 column line.
		s = "\n" + strings.Repeat(" ", 68) + s
	}
	a.out.raw(s)
	a.bar.pos = 6
}

// hint reports n bytes of progress; bars of unknown size grow over time.
func (a *app) hint(n int64) {
	if b := a.bar; b != nil {
		b.update(n, false)
	}
}

// animate redraws the bar on every tick until it ends, for work that blocks.
func (a *app) animate() {
	b := a.bar
	if b == nil {
		return
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				b.update(0, true)
			}
		}
	})
	b.stop = func() {
		close(done)
		wg.Wait()
	}
}

func (b *bar) update(n int64, ticked bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.done += n
	if now := b.a.now(); ticked || now.Sub(b.last) >= tick {
		b.last, ticked = now, true
	}
	seg := 1
	if b.total > 0 {
		seg = min(max(int(b.done*13/b.total/2), 1), 6)
		if seg == b.seg && !ticked {
			return
		}
	} else {
		if !ticked {
			return
		}
		b.ticks++
		for _, t := range []int{10, 30, 90, 180} {
			if b.ticks > t {
				seg++
			}
		}
	}
	b.seg = seg
	s := strings.Repeat("\b", b.pos) + b.a.deco(strings.Repeat("■", seg)+strings.Repeat("·", 6-seg)) + " " + string(`|/-\`[b.spin%4]) + "\b"
	b.spin++
	b.a.out.raw(s)
	b.pos = 7
}

// endBar leaves a full bar; the next output starts at its 8th column, as
// in UC2, which gives "■■■■■■ DONE" and "■■■■■■   OK" for "  OK".
func (a *app) endBar() {
	b := a.bar
	if b == nil {
		return
	}
	if b.stop != nil {
		b.stop()
	}
	a.out.raw(strings.Repeat("\b", b.pos) + a.deco("■■■■■■") + "  \b")
	a.bar = nil
}

// phase prints a phase line of level l, such as "Scanning ", shows a bar
// of unknown size while fn (if any) runs and ends the line.
func (a *app) phase(l int, msg string, fn func() error) error {
	a.outl(l, cN+"%s ", msg)
	a.startBar(l, -1)
	var err error
	if fn != nil {
		a.animate()
		err = fn()
	}
	a.endBar()
	a.endLine(l)
	return err
}

// hinter reports the bytes written through it to the progress bar.
type hinter struct {
	w io.Writer
	a *app
}

func (h hinter) Write(p []byte) (int, error) {
	n, err := h.w.Write(p)
	h.a.hint(int64(n))
	return n, err
}
