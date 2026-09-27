// Package oracle runs the original DOS UltraCompressor II executables
// headless in DOSBox-X, so tests can compare against the real thing.
//
// Set UC2_DOSBOX to a DOSBox-X mingw zip or dosbox-x.exe. Downloads and
// installs are cached in UC2_ORACLE_CACHE (default <UserCacheDir>/uc2-oracle).
package oracle

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/uc2/internal/charset"
)

// Versions of UC2 the oracle can run.
const (
	R2   = "r2"    // UltraCompressor II revision 2 (1994), the published source
	V23  = "2.3"   // UC2 PRO 2.3 ("revision 3", 1995)
	V237 = "2.37b" // 2.37 beta (1996), stores Win95 long names
)

// DefaultTimeout applies to Run when ctx has no deadline.
const DefaultTimeout = 3 * time.Minute

// Env is a prepared oracle environment.
type Env struct {
	dosbox string
	dirs   map[string]string // version -> install dir
	errs   map[string]error
	sem    chan struct{}
}

var setup = sync.OnceValues(prepare)

// New returns the shared Env. It skips t if UC2_DOSBOX is unset.
func New(t testing.TB) *Env {
	t.Helper()
	if os.Getenv("UC2_DOSBOX") == "" {
		t.Skip("UC2_DOSBOX not set")
	}
	e, err := setup()
	if err != nil {
		t.Fatal("oracle:", err)
	}
	for _, v := range slices.Sorted(maps.Keys(e.errs)) {
		t.Logf("oracle: %s unavailable: %v", v, e.errs[v])
	}
	if len(e.dirs) == 0 {
		t.Fatal("oracle: no UC2 version available")
	}
	return e
}

// Versions returns the available versions, oldest first.
func (e *Env) Versions() []string {
	var v []string
	for _, s := range []string{R2, V23, V237} {
		if e.dirs[s] != "" {
			v = append(v, s)
		}
	}
	return v
}

// Run executes DOS command lines in order with work mounted as C: (current
// directory C:\) and the UC2 installation on U: (in PATH). It stops at the
// first command with a non-zero errorlevel and returns that level and the
// stdout of all commands, CP437 decoded. UC2_ANONYMOUS=1 and UC2_OK=OFF are set.
// Commands must not redirect output and must never wait for input (use the
// F option where UC2 could ask), or they hang until the timeout.
func (e *Env) Run(ctx context.Context, ver, work string, cmds ...string) (rc int, out string, err error) {
	dir := e.dirs[ver]
	if dir == "" {
		return 0, "", fmt.Errorf("oracle: version %q not available", ver)
	}
	if work, err = filepath.Abs(work); err != nil {
		return 0, "", err
	}
	// UC.EXE may rewrite itself and AIP-NL.INI, so every run gets a private copy.
	u, err := os.MkdirTemp("", "uc2oracle-")
	if err != nil {
		return 0, "", err
	}
	defer os.RemoveAll(u)
	if err := copyDir(u, dir); err != nil {
		return 0, "", err
	}
	return e.dosboxRun(ctx, work, u, cmds)
}

// dosboxRun runs cmds with C: = work and U: = u. RUN.BAT, OUT.TXT, RC.TXT
// and TMP\ are created in u.
func (e *Env) dosboxRun(ctx context.Context, work, u string, cmds []string) (int, string, error) {
	e.sem <- struct{}{}
	defer func() { <-e.sem }()

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}
	dl, _ := ctx.Deadline()

	var b strings.Builder
	b.WriteString("set UC2_ANONYMOUS=1\r\nset UC2_OK=OFF\r\nset UC2_TMP=U:\\TMP\r\nset PATH=U:\\;Z:\\\r\nset R=0\r\nc:\r\ncd \\\r\n")
	for _, c := range cmds {
		fmt.Fprintf(&b, "%s >> U:\\OUT.TXT\r\n", c)
		// %ERRORLEVEL% is not expanded by the DOSBox-X shell.
		for i := 1; i < 256; i++ {
			fmt.Fprintf(&b, "@if errorlevel %d set R=%d\r\n", i, i)
		}
		b.WriteString("@if errorlevel 1 goto end\r\n@echo. >> U:\\OUT.TXT\r\n")
	}
	b.WriteString(":end\r\necho RC %R% > U:\\RC.TXT\r\n")
	conf := fmt.Sprintf("[dosbox]\nmemsize=16\n[cpu]\ncycles=max\n[dos]\nver=7.1\nlfn=true\n[autoexec]\nmount c \"%s\"\nmount u \"%s\"\ncall U:\\RUN.BAT\n", work, u)
	for name, data := range map[string]string{"RUN.BAT": b.String(), "dosbox.conf": conf} {
		if err := os.WriteFile(filepath.Join(u, name), []byte(data), 0o644); err != nil {
			return 0, "", err
		}
	}
	if err := os.MkdirAll(filepath.Join(u, "TMP"), 0o755); err != nil {
		return 0, "", err
	}

	limit := int(time.Until(dl).Seconds()) + 5
	cmd := exec.CommandContext(ctx, e.dosbox, "-silent", "-fastlaunch", "-nopromptfolder", "-log-con",
		"-time-limit", fmt.Sprint(limit), "-conf", filepath.Join(u, "dosbox.conf"))
	cmd.Dir = u
	log, runErr := cmd.CombinedOutput()
	out, _ := os.ReadFile(filepath.Join(u, "OUT.TXT"))
	var rc int
	rcb, err := os.ReadFile(filepath.Join(u, "RC.TXT"))
	if err == nil {
		_, err = fmt.Sscanf(string(rcb), "RC %d", &rc)
	}
	if err != nil {
		return 0, decode(out), fmt.Errorf("oracle: no exit code (dosbox: %v, ctx: %v)\n%s", runErr, ctx.Err(), conLog(log))
	}
	return rc, decode(out), nil
}

// conLog returns the tail of the DOS console log for diagnostics.
func conLog(log []byte) string {
	var l []string
	for s := range strings.Lines(string(log)) {
		if c, ok := strings.CutPrefix(s, "LOG: DOS CON: "); ok {
			l = append(l, strings.TrimRight(c, "\r\n"))
		}
	}
	return strings.Join(l[max(0, len(l)-30):], "\n")
}

func decode(b []byte) string {
	b = bytes.ReplaceAll(b, []byte("\r"), nil)
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(charset.CP437.DecodeByte(c))
	}
	return sb.String()
}

func copyDir(dst, src string) error {
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, de := range ents {
		if de.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, de.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, de.Name()), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}
