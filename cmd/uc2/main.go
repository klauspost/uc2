// Command uc2 creates, lists, extracts, tests and repairs UltraCompressor II
// archives with the command line syntax of the original DOS program.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/klauspost/uc2"
	"github.com/klauspost/uc2/cmd/uc2/internal/term"
)

// Exit codes (severity levels) of the original UC2.
const (
	sevMapped     = 10
	sevNotSmaller = 15
	sevNoMatch    = 20
	sevSkipped    = 30
	sevDelete     = 55
	sevRmdir      = 60
	sevWrite      = 80
	sevMkdir      = 85
	sevDamaged    = 90
	sevAbort      = 100
	sevEditor     = 115
	sevCmdLine    = 120
	sevEncrypted  = 125
	sevNoArchive  = 130
	sevVersion    = 145
	sevFix        = 150
	sevChdir      = 185
	sevBroken     = 200
	sevInternal   = 255
)

const (
	quiet = iota
	normal
	verbose
)

type fatalError struct {
	code  int
	msg   string
	cause string // reported as ERROR 90 before the fatal error, if set
}

func (e *fatalError) Error() string { return e.msg }

func fatalf(code int, format string, args ...any) error {
	return &fatalError{code: code, msg: fmt.Sprintf(format, args...)}
}

type app struct {
	in          *bufio.Reader
	rawIn       io.Reader
	out, errOut *painter  // stdout and stderr
	stdout      io.Writer // the raw streams
	stderr      io.Writer
	tty         bool // line prompts are possible: stdin and stderr are terminals
	inTTY       bool
	console     bool // stderr is a terminal: the console may be opened for key prompts
	termOut     bool // stdout is a terminal: progress bars are drawn
	vtOut       bool // stdout takes VT sequences, known after initConsole
	noHigh      bool // UC2_NO_HIGH_ASCII
	verbosity   int

	key     func() ([]byte, error) // reads a key press, nil for line input
	keys    *term.Keys             // reads stdin from the first full-screen session on
	openTUI func() (*tui, error)   // opens the terminal for the full-screen help
	unraw   func()                 // ends raw mode while a key is read; guarded by tempMu
	bar     *bar
	restore []func() // restores the console at exit
	now     func() time.Time
	sleep   func(time.Duration)

	severity, errors, warnings int
	dump                       bool // a ~ command ran: U$~RESLT.OK ends the run
	headers                    int
	overwrite                  int
	mapped                     map[string]bool // Windows name mappings reported
	stdinComment               *string         // comment read from standard input
}

const (
	overwriteAsk = iota
	overwriteAll
	overwriteNone
)

func main() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, abortSignals...)
	go onSignal(sig, os.Stderr, os.Exit)
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func newApp(stdin io.Reader, stdout, stderr io.Writer) *app {
	a := &app{rawIn: stdin, out: &painter{w: bufio.NewWriter(stdout)}, errOut: &painter{w: stderr},
		stdout: stdout, stderr: stderr, verbosity: normal, now: time.Now, sleep: time.Sleep}
	a.in = bufio.NewReader(stdinReader{a})
	a.openTUI = a.openTTY
	return a
}

// temp is a temporary file to remove when the program is interrupted.
type temp struct {
	f      *os.File
	remove func() error
}

var (
	tempMu  sync.Mutex
	temps   = map[*temp]bool{}
	aborter func() // reports an interrupt through the running app
)

// track registers an open temporary file; the returned function
// unregisters it. Unregister only after the file is renamed or removed.
func track(f *os.File, remove func() error) func() {
	t := &temp{f, remove}
	tempMu.Lock()
	temps[t] = true
	tempMu.Unlock()
	return func() {
		tempMu.Lock()
		delete(temps, t)
		tempMu.Unlock()
	}
}

// onSignal removes the temporary files and exits when a signal arrives.
// The lock stays held so that no new temporary file appears before exit,
// which only returns in tests.
func onSignal(sig <-chan os.Signal, stderr io.Writer, exit func(int)) {
	<-sig
	tempMu.Lock()
	defer tempMu.Unlock()
	for t := range temps {
		t.f.Close() // Windows cannot remove open files
		// A write in progress can keep the file open a little longer.
		for range 20 {
			if err := t.remove(); err == nil || errors.Is(err, fs.ErrNotExist) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if aborter != nil {
		aborter()
	} else {
		fmt.Fprintf(stderr, "\nFATAL ERROR %d: program aborted by user\n", sevAbort)
	}
	exit(sevAbort)
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := newApp(stdin, stdout, stderr)
	a.inTTY = isTerminal(stdin)
	a.console = isTerminal(stderr)
	a.tty = a.inTTY && a.console
	a.termOut = isTerminal(stdout)
	tempMu.Lock()
	aborter = a.abort
	tempMu.Unlock()
	defer func() {
		tempMu.Lock()
		aborter = nil
		tempMu.Unlock()
	}()
	return a.run(args)
}

// abort reports an interrupt. It runs on the signal goroutine while the
// program may still be working; the process exits right after.
func (a *app) abort() {
	if a.unraw != nil {
		a.unraw()
	}
	a.errors++
	fmt.Fprintf(a.errOut, "\n"+cErr+"FATAL ERROR %d: program aborted by user\n", sevAbort)
	if a.dump {
		dumpResult(false)
	} else {
		a.summary()
	}
	a.restoreConsole()
}

func (a *app) run(args []string) (code int) {
	defer a.restoreConsole()
	defer a.out.Flush()
	defer func() {
		if p := recover(); p != nil {
			a.fatal(fatalf(sevInternal, "internal error: %v", p))
			code = a.finish()
		}
	}()
	if len(args) == 0 || isHelpWord(args[0]) {
		return a.helpCmd(args)
	}
	cmds, g, err := parseArgs(args)
	a.verbosity = g.verbosity
	if g.help || g.version {
		a.initConsole("never")
	} else {
		a.initConsole(g.color)
	}
	// UC2 shows no logo when the first word is a ~ command.
	word := commandWord(args)
	tilde := err == nil && len(cmds) > 0 && cmds[0].op == '~' || err != nil && strings.HasPrefix(word, "~")
	switch {
	case err != nil:
		if !tilde {
			a.logo()
		}
		a.dump = tilde && isDumpCommand(word)
		a.fatal(err)
		return a.finish()
	case g.help || len(cmds) == 0 && !g.version:
		a.help()
		return 0
	case g.version:
		fmt.Fprintf(a.out, "uc2 %s (UltraCompressor II revision 2 compatible)\n", version())
		return 0
	}
	if !tilde {
		a.logo()
	}
	for _, c := range cmds {
		if err := a.exec(c); err != nil {
			a.fatal(err)
			break
		}
	}
	return a.finish()
}

// buildVersion is set by release builds (-X main.buildVersion=...).
var buildVersion string

func version() string {
	if buildVersion != "" {
		return "v" + strings.TrimPrefix(buildVersion, "v")
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "devel"
}

func (a *app) finish() int {
	switch {
	case a.dump:
		a.breakLine()
		dumpResult(a.errors+a.warnings == 0)
	case a.errors+a.warnings == 0:
		a.printf("\n" + cOK + "Everything went OK\n")
	default:
		a.breakLine()
		a.summary()
	}
	return a.severity
}

// summary reports the number of errors and warnings on stderr.
func (a *app) summary() {
	noun := func(n int, what string) string {
		if n == 1 {
			return "1 " + what
		}
		return fmt.Sprintf("%d %ss", n, what)
	}
	have := func(n int) string {
		if n == 1 {
			return " has"
		}
		return " have"
	}
	var s string
	switch {
	case a.errors > 0 && a.warnings > 0:
		s = noun(a.errors, "error") + " and " + noun(a.warnings, "warning") + " have"
	case a.errors > 0:
		s = noun(a.errors, "error") + have(a.errors)
	default:
		s = noun(a.warnings, "warning") + have(a.warnings)
	}
	fmt.Fprintf(a.errOut, cErr+"\n%s been reported \n", s)
}

func (a *app) exec(c *cmd) error {
	a.overwrite, a.mapped = overwriteAsk, map[string]bool{}
	if c.op == '~' {
		return a.tilde(c)
	}
	for _, arch := range a.archives(c) {
		var err error
		switch c.op {
		case 'A':
			err = a.add(c, arch)
		case 'D':
			err = a.delete(c, arch)
		case 'E':
			err = a.extract(c, arch)
		case 'L', 'V':
			err = a.list(c, arch)
		case 'T':
			err = a.test(c, arch)
		case 'P', 'U':
			err = a.protect(c, arch)
		case 'O':
			err = a.optimize(c, arch)
		case 'R':
			err = a.comment(c, arch)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// clean replaces control characters, with which names, labels and error
// texts from hostile archives could send terminal escape sequences, and
// format characters such as bidi overrides, which disguise names.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r >= 0x7f && r < 0xa0 || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return '?'
		}
		return r
	}, s)
}

// cleanArgs cleans the text arguments of a format; format strings are trusted.
func cleanArgs(args []any) []any {
	for i, v := range args {
		switch x := v.(type) {
		case string:
			args[i] = clean(x)
		case error:
			args[i] = clean(x.Error())
		}
	}
	return args
}

// printf prints at normal and verbose level (UC2 level 3).
func (a *app) printf(format string, args ...any) { a.outl(lvStd, format, args...) }

// normalf prints at normal level only (UC2 level 2).
func (a *app) normalf(format string, args ...any) { a.outl(lvNormal, format, args...) }

func (a *app) quietf(format string, args ...any) { a.outl(lvQuiet, format, args...) }

func (a *app) verbosef(format string, args ...any) { a.outl(lvVerbose, format, args...) }

// outf prints requested output (listings), regardless of the verbosity.
func (a *app) outf(format string, args ...any) { a.outl(lvAll, format, args...) }

func (a *app) report(format string, code int, msg string) {
	a.breakLine()
	fmt.Fprintf(a.errOut, format, code, clean(msg))
	a.severity = max(a.severity, code)
}

func (a *app) warnf(code int, format string, args ...any) {
	a.warnings++
	a.report(cErr+" WARNING %d: %s\n", code, fmt.Sprintf(format, args...))
}

func (a *app) errorf(code int, format string, args ...any) {
	a.errors++
	a.report(cErr+" ERROR %d: %s\n", code, fmt.Sprintf(format, args...))
}

func (a *app) fatal(err error) {
	var fe *fatalError
	if !errors.As(err, &fe) {
		fe = &fatalError{code: sevInternal, msg: err.Error()}
	}
	if fe.cause != "" {
		a.errorf(sevDamaged, "%s", fe.cause)
	}
	a.errors++
	a.report("\n"+cErr+"FATAL ERROR %d: %s\n", fe.code, fe.msg)
}

// header announces the archive being processed, like UC2's Arch().
func (a *app) header(verb string, c *cmd, arch string) {
	if a.headers++; a.headers > 1 {
		a.outf("\n")
	}
	switch {
	case c.destSrc && c.dest != "":
		a.printf(cOK+"%s %s (destination path %s+<sourcepath>)\n", verb, arch, dispDir(c.dest))
	case c.destSrc:
		a.printf(cOK+"%s %s (destination path <sourcepath>)\n", verb, arch)
	case c.dest != "":
		a.printf(cOK+"%s %s (destination path %s)\n", verb, arch, dispDir(c.dest))
	default:
		a.printf(cOK+"%s %s\n", verb, arch)
	}
	a.quietf(cOK+"%s\n", arch)
}

// dispDir formats a directory path DOS style, with a trailing backslash.
func dispDir(d string) string {
	d = strings.ReplaceAll(filepath.ToSlash(d), "/", `\`)
	if !strings.HasSuffix(d, `\`) {
		d += `\`
	}
	return d
}

// damaged reports whether err indicates a corrupt archive.
func damaged(err error) bool {
	return errors.Is(err, uc2.ErrFormat) || errors.Is(err, uc2.ErrChecksum) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// archiveErr converts an error about an archive to a fatal error.
func archiveErr(arch string, err error) error {
	var fe *fatalError
	switch {
	case errors.As(err, &fe):
		return err
	case errors.Is(err, fs.ErrNotExist):
		return fatalf(sevNoArchive, "%s does not exist", arch)
	case errors.Is(err, errors.ErrUnsupported) && strings.Contains(err.Error(), "encrypted"):
		return fatalf(sevEncrypted, "%s is encrypted with UltraCrypt", arch)
	case errors.Is(err, errors.ErrUnsupported):
		return fatalf(sevVersion, "%s: %v", arch, err)
	case damaged(err):
		return &fatalError{code: sevBroken, msg: "you should repair this archive with 'uc2 T'",
			cause: fmt.Sprintf("archive %s is damaged (%v)", arch, err)}
	case errors.As(err, new(*fs.PathError)):
		return fatalf(sevNoArchive, "failed to access archive %s (%v)", arch, err)
	}
	return fatalf(sevInternal, "%s: %v", arch, err)
}

// writeErr converts an error while writing an archive to a fatal error.
func writeErr(arch string, err error) error {
	var fe *fatalError
	if errors.As(err, &fe) || errors.Is(err, errors.ErrUnsupported) || damaged(err) {
		return archiveErr(arch, err)
	}
	return fatalf(sevWrite, "cannot write %s (%v)", arch, err)
}

// archive is an open archive and its file as it was when opened.
type archive struct {
	*uc2.ReadCloser
	info    fs.FileInfo
	checked bool
}

func (a *app) openArchive(c *cmd, arch string) (*archive, error) {
	// Stat before opening: a replacement in between is then detected as a change.
	fi, err := os.Stat(arch)
	if err != nil {
		return nil, archiveErr(arch, err)
	}
	os.SameFile(fi, fi) // pin the Windows file ID; it is otherwise read by path when compared
	r, err := uc2.OpenReader(arch, c.opts(nil)...)
	if err != nil {
		return nil, archiveErr(arch, err)
	}
	return &archive{ReadCloser: r, info: fi}, nil
}

// check verifies the archive structure once. Rewrites copy compressed data
// unverified; fresh damage protection over damaged data would destroy the
// records that can still repair it.
func (r *archive) check(arch string) error {
	if !r.checked {
		if err := r.Check(); err != nil {
			return archiveErr(arch, err)
		}
		r.checked = true
	}
	return nil
}

// unchanged reports whether cur is still the file was: same file, size and time.
func unchanged(cur, was fs.FileInfo) bool {
	return os.SameFile(cur, was) && cur.Size() == was.Size() && cur.ModTime().Equal(was.ModTime())
}
