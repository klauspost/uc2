package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/uc2/cmd/uc2/internal/term"
)

func (a *app) help() {
	a.outf(`UltraCompressor II (Go port), reads and writes UC2 revision 2 archives

SYNTAX: uc2 command[options] [options] archive[.UC2] [files...] [& command ...]

COMMANDS: A M F D E   add / move / freshen / delete / extract (also X)
              L V     list / verbose list (all revisions)
              P U     damage protect / unprotect
                T     test (& repair into FIX_nnnn.UC2)
                O     optimize (recompress, TT by default)
                R     revise archive comment

OPTIONS: (directly after command, or preceded by '-' or, on Windows, '/')
      TF TN TT TST    fast / normal / tight / super tight compression
                 S    include subdirectories
                 M    move mode (delete files after adding / extracting)
                 F    force mode (never ask, always overwrite)
               I B    incremental mode (keep versions) / basic mode
               P U    add / remove damage protection while writing
            !NEWER    only files newer than their counterpart

  ;n specify version   ;* all versions   !exclude files   #destination
  ##[dest] destination + source path     & concat commands   @script

LONG FORMS: --recurse (-r) --move --force (-f) --incremental (-i) --basic
  --protect --unprotect --newer --level=fast|normal|tight|super --dest=DIR (-d)
  --exclude=PAT (-x) --rev=N|all --charset=437|850 --threads=N
  --comment-file=FILE --color=auto|always|never --verbose (-v) --quiet (-q)
  --help (-h) --version
  Commands: add move freshen delete extract list verbose test protect
  unprotect optimize comment. Everything after -- is a file name.
  Put -- before shell wildcards: uc2 a arch -- *

Not supported: C (convert), $ commands, !DTT, !CONTAINS, !QUERY,
  lock files, !VLAB, !RELIA, banners.
`)
}

// helpCmd runs "uc2" and the help words. On a terminal, "uc2" opens UC2's
// help menu and "uc2 -? words" opens it with the words typed ahead as a
// search (MAIN.CPP:1315-1337). Otherwise, for a help word alone, and for
// --help and -help, it prints the mini help.
func (a *app) helpCmd(args []string) int {
	switch {
	case len(args) == 1, len(args) > 1 && (strings.EqualFold(args[0], "--help") || optionPrefix(args[0]) && strings.EqualFold(args[0][1:], "help")), !a.canMenu():
		a.help()
		return 0
	}
	a.initConsole("auto")
	t, err := a.openTUI()
	if err != nil {
		a.help()
		return 0
	}
	end := a.session(t)
	defer t.close(false)
	h := &help{a: a, t: t, docs: manual()}
	if len(args) > 1 {
		h.queue = searchKeys(args[1:])
	}
	h.menu()
	end()
	return a.finish()
}

// searchKeys returns the keys that "uc2 -? words" types ahead: a document,
// then S, the words and Enter. An error number searches the error list,
// paragraph 8.G of the 2.4 manual (8.F in UC2 revision 2).
func searchKeys(words []string) []term.Key {
	keys := []term.Key{'1', 'S'}
	if atoi(words[0]) != 0 && (len(words[0]) < 2 || words[0][1] != '.') {
		keys = []term.Key{'8', 'G', 'S'}
	}
	for _, r := range asciiUpper(strings.Join(words, " ")) {
		keys = append(keys, term.Key(r))
	}
	return append(keys, term.KeyEnter)
}

// atoi is C's atoi, with which UC2 told numbers from words.
func atoi(s string) int {
	s = strings.TrimLeft(s, " \t")
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}

// menuItems describe the chapters in the menu.
var menuItems = [...]string{
	"what changed from UC2 2.3 to 2.4",
	"how to get started, overview, features etc.",
	"the license (LGPL), no warranty",
	"the most essential commands of UC",
	"the use of the UC command in detail",
	"special features for BBS sysops",
	"how to configure UC",
	"concepts, program design, compressor design, benchmarks",
	"tools; extended commands and options",
}

// menu runs the help menu (MAIN.CPP HelpMenu) until Esc, Q or X, or until
// Tab in the viewer shows a summary. Both leave the screen as it is.
func (h *help) menu() {
	opt, redraw := 3, true
	h.t.clear()
	for {
		if redraw {
			h.drawMenu(opt)
		}
		redraw = true
		k := h.key()
		switch {
		case k == term.KeyResize:
			continue
		case k == keyEOF:
			h.t.write("\x1b[0m\r\n")
			return
		case h.t.small() && k != term.KeyEsc && k != 'Q' && k != 'X' && k != term.KeyCtrlC:
			redraw = false
			continue
		}
		h.echo(k)
		switch {
		case k == term.KeyUp:
			opt = (opt + 8) % 9
		case k == term.KeyDown:
			opt = (opt + 1) % 9
		case k == term.KeyEnter || k >= '0' && k <= '8':
			d := opt
			if k != term.KeyEnter {
				d = int(k - '0')
			}
			if h.view(d) {
				return
			}
		case k == term.KeyEsc || k == 'Q' || k == 'X' || k == term.KeyCtrlC:
			h.t.write("\x1b[0m\r\n")
			return
		default:
			h.t.write(h.t.paint(cErr + " *** WRONG CHOICE ***\r"))
			h.a.sleep(500 * time.Millisecond)
			h.t.write(h.t.paint(cN+strings.Repeat(" ", 57)) + "\r" + h.prompt())
			redraw = false
		}
	}
}

// drawMenu draws the menu from the top of the screen, with item opt
// highlighted and the cursor after the prompt.
func (h *help) drawMenu(opt int) {
	t := h.t
	if t.small() {
		t.draw(nil)
		return
	}
	rows := append(h.a.logoLines(), "", cAsk+"HELP MENU "+cN+"(view"+cT+" & search "+cN+"documentation)")
	for i, s := range menuItems {
		c := cN
		if i == opt {
			c = cAsk
		}
		rows = append(rows, fmt.Sprintf("  "+cAsk+" %d%s   -> %-8s  %s", i, c, docNames[i], s))
	}
	var b strings.Builder
	b.WriteString(curOff)
	for i, r := range append(rows, "") {
		fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[0m\x1b[K", i+1, t.paint(r))
	}
	fmt.Fprintf(&b, "\x1b[%d;1H%s%s", len(rows)+2, h.prompt(), curOn)
	t.write(b.String())
}

// prompt prints the menu's prompt over the current row, with "Mini help"
// on the right, and leaves the cursor after the question.
func (h *help) prompt() string {
	const q = cAsk + "CHOICE (Escape to quit)?"
	return h.t.paint(q+strings.Repeat(" ", 38)+cN+"Mini help: "+cT+"uc2 -?") + "\x1b[0m\x1b[K\r" + h.t.paint(q+" ")
}

// echo shows a key as UC2's Echo did, in the prompt's yellow.
func (h *help) echo(k term.Key) {
	var s string
	switch {
	case k == term.KeyEnter:
		s = "[Enter]"
	case k == term.KeyEsc:
		s = "[Esc]"
	case k < ' ' || k == 0x7f:
		s = h.a.deco("■") // control and special keys
	default:
		s = clean(string(rune(k)))
	}
	h.t.write(h.t.paint(cAsk + s))
	h.a.sleep(50 * time.Millisecond)
}
