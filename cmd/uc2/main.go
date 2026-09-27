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
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/klauspost/uc2"
)

// Exit codes (severity levels) of the original UC2.
const (
	sevMapped     = 10
	sevNotSmaller = 15
	sevNoMatch    = 20
	sevSkipped    = 30
	sevDelete     = 55
	sevWrite      = 80
	sevDamaged    = 90
	sevAbort      = 100
	sevEditor     = 115
	sevCmdLine    = 120
	sevEncrypted  = 125
	sevNoArchive  = 130
	sevVersion    = 145
	sevFix        = 150
	sevBroken     = 200
	sevInternal   = 255
)

const (
	quiet = iota
	normal
	verbose
)

type fatalError struct {
	code int
	msg  string
}

func (e *fatalError) Error() string { return e.msg }

func fatalf(code int, format string, args ...any) error {
	return &fatalError{code, fmt.Sprintf(format, args...)}
}

type app struct {
	in        *bufio.Reader
	rawIn     io.Reader
	out       *bufio.Writer
	stderr    io.Writer
	tty       bool // prompts are possible: stdin and stderr are terminals
	inTTY     bool
	verbosity int

	severity, errors, warnings int
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

// temp is a temporary file to remove when the program is interrupted.
type temp struct {
	f      *os.File
	remove func() error
}

var (
	tempMu sync.Mutex
	temps  = map[*temp]bool{}
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
		for i := 0; i < 20; i++ {
			if err := t.remove(); err == nil || errors.Is(err, fs.ErrNotExist) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	fmt.Fprintf(stderr, "\nFATAL ERROR %d: program aborted by user\n", sevAbort)
	exit(sevAbort)
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := &app{in: bufio.NewReader(stdin), rawIn: stdin, out: bufio.NewWriter(stdout), stderr: stderr, verbosity: normal}
	a.inTTY = isTerminal(stdin)
	a.tty = a.inTTY && isTerminal(stderr)
	return a.run(args)
}

func isTerminal(x any) bool {
	f, ok := x.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&fs.ModeCharDevice != 0
}

func (a *app) run(args []string) (code int) {
	defer a.out.Flush()
	defer func() {
		if p := recover(); p != nil {
			a.fatal(fatalf(sevInternal, "internal error: %v", p))
			code = a.finish()
		}
	}()
	if len(args) == 0 || isHelpWord(args[0]) {
		a.help()
		return 0
	}
	cmds, g, err := parseArgs(args)
	a.verbosity = g.verbosity
	switch {
	case err != nil:
		a.fatal(err)
		return a.finish()
	case g.help || len(cmds) == 0 && !g.version:
		a.help()
		return 0
	case g.version:
		fmt.Fprintf(a.out, "uc2 %s (UltraCompressor II revision 2 compatible)\n", version())
		return 0
	}
	a.verbosef("UltraCompressor II Go port %s\n", version())
	for _, c := range cmds {
		if err := a.exec(c); err != nil {
			a.fatal(err)
			break
		}
	}
	return a.finish()
}

func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "devel"
}

func (a *app) finish() int {
	if a.errors+a.warnings == 0 {
		a.printf("\nEverything went OK\n")
		return a.severity
	}
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
	a.out.Flush()
	fmt.Fprintf(a.stderr, "\n%s been reported\n", s)
	return a.severity
}

func (a *app) exec(c *cmd) error {
	a.overwrite, a.mapped = overwriteAsk, map[string]bool{}
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

func (a *app) printf(format string, args ...any) {
	if a.verbosity >= normal {
		fmt.Fprintf(a.out, format, cleanArgs(args)...)
	}
}

func (a *app) quietf(format string, args ...any) {
	if a.verbosity == quiet {
		fmt.Fprintf(a.out, format, cleanArgs(args)...)
	}
}

func (a *app) verbosef(format string, args ...any) {
	if a.verbosity >= verbose {
		fmt.Fprintf(a.out, format, cleanArgs(args)...)
	}
}

// outf prints requested output (listings), regardless of the verbosity.
func (a *app) outf(format string, args ...any) { fmt.Fprintf(a.out, format, cleanArgs(args)...) }

// say prints a per-file progress line: long at normal verbosity, short when quiet.
func (a *app) say(long, short, name string) {
	a.printf(long+"\n", name)
	if short != "" {
		a.quietf(short+"\n", name)
	}
}

func (a *app) report(kind string, code int, msg string) {
	a.out.Flush()
	fmt.Fprintf(a.stderr, "%s %d: %s\n", kind, code, clean(msg))
	a.severity = max(a.severity, code)
}

func (a *app) warnf(code int, format string, args ...any) {
	a.warnings++
	a.report(" WARNING", code, fmt.Sprintf(format, args...))
}

func (a *app) errorf(code int, format string, args ...any) {
	a.errors++
	a.report(" ERROR", code, fmt.Sprintf(format, args...))
}

func (a *app) fatal(err error) {
	var fe *fatalError
	if !errors.As(err, &fe) {
		fe = &fatalError{sevInternal, err.Error()}
	}
	a.errors++
	a.report("FATAL ERROR", fe.code, fe.msg)
}

// header announces the archive being processed, like UC2's Arch().
func (a *app) header(verb string, c *cmd, arch string) {
	if a.headers++; a.headers > 1 {
		a.printf("\n")
	}
	switch {
	case c.destSrc && c.dest != "":
		a.printf("%s %s (destination path %s+<sourcepath>)\n", verb, arch, c.dest)
	case c.destSrc:
		a.printf("%s %s (destination path <sourcepath>)\n", verb, arch)
	case c.dest != "":
		a.printf("%s %s (destination path %s)\n", verb, arch, c.dest)
	default:
		a.printf("%s %s\n", verb, arch)
	}
	a.quietf("%s\n", arch)
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
		return fatalf(sevBroken, "archive %s is damaged (%v), repair it with 'uc2 T'", arch, err)
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
