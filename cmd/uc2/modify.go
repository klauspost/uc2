package main

import (
	"cmp"
	"errors"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/uc2"
)

// createTemp creates a U$~XXXXX.TMP file in dir, named like UC2's temporary files.
func createTemp(dir string, perm fs.FileMode) (*os.File, error) {
	var err error
	for range 100 {
		b := make([]byte, 5)
		for i := range b {
			b[i] = 'A' + byte(rand.IntN(26))
		}
		var f *os.File
		f, err = os.OpenFile(filepath.Join(dir, "U$~"+string(b)+".TMP"), os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
	return nil, err
}

// buildTemp writes a new archive, protected or not, to a temporary file
// next to arch and returns its name and the file it replaces: arch with
// symlinks resolved, so that a link to the archive stays a link. Call
// untrack after the temporary file is renamed or removed.
func (a *app) buildTemp(arch string, opts []uc2.Option, protected bool, fill func(*uc2.Writer, *tally) error) (tmp, dst string, untrack func(), err error) {
	dst = arch
	if p, err := filepath.EvalSymlinks(arch); err == nil {
		dst = p
	}
	// The copy is never more accessible than the original.
	perm := fs.FileMode(0o666)
	fi, serr := os.Stat(dst)
	if serr == nil {
		perm = fi.Mode().Perm()
	}
	dir := filepath.Dir(dst)
	f, err := createTemp(dir, perm)
	if err != nil {
		return "", "", nil, fatalf(sevWrite, "cannot create a temporary file in %s (%v)", dir, err)
	}
	untrack = track(f, func() error { return os.Remove(f.Name()) })
	w := uc2.NewWriter(f, opts...)
	t := &tally{}
	if err = fill(w, t); err == nil {
		err = a.closeArchive(w, t, protected)
	}
	if err == nil && serr == nil && runtime.GOOS != "windows" {
		err = f.Chmod(perm) // undo the umask
	}
	if err == nil && serr == nil {
		if err := keepOwner(f, fi); err != nil {
			a.warnf(sevSkipped, "cannot keep the owner of %s (%v)", arch, err)
		}
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		untrack()
		return "", "", nil, writeErr(arch, err)
	}
	return f.Name(), dst, untrack, nil
}

// tally counts the entries of an archive being written, for UC2's
// "Updated archive" line, which comes before the writer finishes.
type tally struct {
	files, dirs, size int64
	compressed        bool // new data, which the writer compresses when it closes
}

func (t *tally) add(f *uc2.File) {
	if isDir(f) {
		t.dirs++
	} else {
		t.files++
		t.size += f.Size
	}
}

// String describes the contents with UC2's spacing (SUPERMAN.CPP fs).
func (t *tally) String() string {
	if t.files == 0 && t.dirs == 0 {
		return "is empty "
	}
	s := "contains "
	if t.files > 0 {
		s += plural(t.files, "file", "files") + " (" + plural(t.size, "byte", "bytes") + ") "
	}
	if t.files > 0 && t.dirs > 0 {
		s += "and "
	}
	switch {
	case t.dirs == 1:
		s += "1 directory"
	case t.dirs > 1:
		s += neat(t.dirs) + " directories "
	}
	return s + " "
}

// unchanged prints the "Updated archive" line for an archive that needs no
// rewrite: UC2 prints it even when nothing changed.
func (a *app) unchanged(r *archive) {
	t := &tally{}
	for _, f := range r.File {
		t.add(f)
	}
	a.printf(cN+"Updated archive %s\n", t.String())
}

// closeArchive finishes w with UC2's closing lines.
func (a *app) closeArchive(w *uc2.Writer, t *tally, protected bool) error {
	a.printf(cN+"Updated archive %s\n", t.String())
	switch {
	case t.compressed:
		if err := a.phase(lvNormal, "Optimizing compression", w.Close); err != nil {
			return err
		}
		if protected {
			a.phase(lvStd, "Damage protecting archive", nil)
		}
		return nil
	case protected:
		return a.phase(lvStd, "Damage protecting archive", w.Close)
	}
	return w.Close()
}

// rewrite replaces arch by a copy of r without the revisions for which drop
// returns true, plus whatever fill adds. The original stays intact until the
// completed copy is renamed over it.
func (a *app) rewrite(arch string, r *archive, drop func(*uc2.File) bool, fill func(*uc2.Writer, *tally) error, opts []uc2.Option, protected bool) error {
	if r != nil {
		if err := r.check(arch); err != nil {
			return err
		}
	}
	tmp, dst, untrack, err := a.buildTemp(arch, opts, protected, func(w *uc2.Writer, t *tally) error {
		if r != nil {
			if r.Label != "" {
				if err := w.SetLabel(r.Label); err != nil {
					return err
				}
			}
			for _, f := range r.File {
				if drop == nil || !drop(f) {
					if err := w.Copy(f); err != nil {
						return err
					}
					t.add(f)
				}
			}
		}
		if fill != nil {
			return fill(w, t)
		}
		return nil
	})
	if err != nil {
		return err
	}
	defer untrack()
	return replace(tmp, dst, r)
}

// replace moves tmp over dst, which must still be the archive r was read
// from; r is nil for a new archive.
func replace(tmp, dst string, r *archive) error {
	var err error
	if r == nil {
		if _, serr := os.Stat(dst); serr == nil {
			err = errors.New("it was created by another process")
		} else {
			err = os.Rename(tmp, dst)
		}
	} else {
		r.Close() // Windows cannot replace an open file.
		if fi, serr := os.Stat(dst); serr != nil || !unchanged(fi, r.info) {
			err = errors.New("it was changed by another process")
		} else {
			err = replaceFile(tmp, dst)
		}
	}
	if err != nil {
		os.Remove(tmp)
		return fatalf(sevWrite, "cannot replace %s (%v)", dst, err)
	}
	return nil
}

// appendErr converts an error opening an archive for an in-place update.
func appendErr(arch string, err error) error {
	if errors.Is(err, uc2.ErrLocked) {
		return fatalf(sevWrite, "cannot update %s (it is being updated by another process)", arch)
	}
	return archiveErr(arch, err)
}

func (a *app) delete(c *cmd, arch string) error {
	a.header("Deleting files from", c, arch)
	r, err := a.openArchive(c, arch)
	if err != nil {
		return err
	}
	defer r.Close()
	masks := compileMasks(c.specs, "")
	drop := map[*uc2.File]bool{}
	for _, s := range newTree(&r.Reader).selectFiles(c, masks, compileExcludes(c.excludes), 0, false) {
		drop[s.f] = true
		// Like E, UC2 shows the path below the mask directory (SUPERMAN.CPP:1515).
		a.printf(cN+"Deleting %s\n", disp(s.rel, s.f.Revision))
		a.quietf(cN+"Delete %s\n", disp(s.rel, s.f.Revision))
	}
	if len(drop) > 0 {
		prot := c.protection(r.Protected)
		if err := a.rewrite(arch, r, func(f *uc2.File) bool { return drop[f] }, nil, c.opts(prot), *prot); err != nil {
			return err
		}
	} else {
		a.unchanged(r)
	}
	a.warnUnmatched(masks)
	return nil
}

func (a *app) protect(c *cmd, arch string) error {
	on := c.op == 'P'
	if on {
		a.header("Damage protecting", c, arch)
	} else {
		a.header("Removing damage protection from", c, arch)
	}
	f, err := os.OpenFile(arch, os.O_RDWR, 0)
	if err != nil {
		return archiveErr(arch, err)
	}
	defer f.Close()
	w, err := uc2.NewAppendWriter(f, c.opts(&on)...)
	if err != nil {
		return appendErr(arch, err)
	}
	t := &tally{}
	if fi, err := f.Stat(); err == nil {
		if r, err := uc2.NewReader(f, fi.Size(), c.opts(nil)...); err == nil {
			for _, e := range r.File {
				t.add(e)
			}
		}
	}
	if err := a.closeArchive(w, t, on); err != nil {
		return writeErr(arch, err)
	}
	return nil
}

// optimize recompresses all revisions, by default at TT, and keeps the
// result only if it is smaller.
func (a *app) optimize(c *cmd, arch string) error {
	a.header("Optimizing", c, arch)
	r, err := a.openArchive(c, arch)
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.check(arch); err != nil {
		return err
	}
	fi, err := os.Stat(arch)
	if err != nil {
		return archiveErr(arch, err)
	}
	oc := *c
	if oc.level == 0 {
		oc.level = uc2.Tight
	}
	prot := c.protection(r.Protected)
	tmp, dst, untrack, err := a.buildTemp(arch, oc.opts(prot), *prot, func(w *uc2.Writer, t *tally) error {
		if r.Label != "" {
			if err := w.SetLabel(r.Label); err != nil {
				return err
			}
		}
		for _, f := range r.File {
			var err error
			if isDir(f) || isInternal(f) {
				err = w.Copy(f)
			} else {
				err = a.recompress(w, f)
				t.compressed = true
			}
			if err != nil {
				return err
			}
			t.add(f)
		}
		return nil
	})
	if err != nil {
		return err
	}
	defer untrack()
	nfi, err := os.Stat(tmp)
	if err != nil {
		os.Remove(tmp)
		return writeErr(arch, err)
	}
	before, after := fi.Size(), nfi.Size()
	if after >= before && *prot == r.Protected {
		os.Remove(tmp)
		a.warnf(sevNotSmaller, "archive size has not changed")
		return nil
	}
	if err := replace(tmp, dst, r); err != nil {
		return err
	}
	left := after * 1000 / before
	a.printf(cOK+"Old = %s bytes, new = %s bytes (%d.%d%% reduction, %d.%d%% left)\n",
		neat(before), neat(after), (1000-left)/10, (1000-left)%10, left/10, left%10)
	return nil
}

// recompress adds f with fresh compression. Writer.Copy would reuse the
// compressed data. The unmodified header keeps the 8.3 name and all tags.
func (a *app) recompress(w *uc2.Writer, f *uc2.File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	a.printf(cN+"Compressing %s ", dispFile(f))
	a.startBar(lvStd, f.Size)
	ew, err := w.CreateHeader(&f.FileHeader)
	if err != nil {
		return err
	}
	if _, err = io.Copy(hinter{ew, a}, rc); err != nil {
		return err
	}
	a.endBar()
	a.printf(cOK + "DONE\n")
	return nil
}

func (a *app) comment(c *cmd, arch string) error {
	a.header("Revise comment from", c, arch)
	r, err := a.openArchive(c, arch)
	if err != nil {
		return err
	}
	defer r.Close()
	old, err := r.Comment()
	if err != nil {
		return archiveErr(arch, err)
	}
	text, err := a.readComment(c, old)
	if err != nil {
		return err
	}
	// DOS comments use CRLF line ends.
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	if text == old {
		a.printf(cN + "Comment is unchanged\n")
		return nil
	}
	isComment := func(f *uc2.File) bool { return strings.EqualFold(f.Name, "U$~COMM.TXT") }
	prot := c.protection(r.Protected)
	return a.rewrite(arch, r, isComment, func(w *uc2.Writer, t *tally) error {
		if text == "" {
			return nil
		}
		a.printf(cN + "Compressing U$~COMM.TXT ")
		a.startBar(lvStd, -1)
		a.endBar()
		a.printf(cOK + "DONE\n")
		t.files++
		t.size += int64(utf8.RuneCountInString(text)) // stored in a single byte code page
		t.compressed = true
		return w.SetComment(text)
	}, c.opts(prot), *prot)
}

// readComment returns the new comment. Standard input is read once and
// applies to every archive; empty input keeps the comment.
func (a *app) readComment(c *cmd, old string) (string, error) {
	switch {
	case c.commentFile == "-" || c.commentFile == "" && !a.inTTY:
		if a.stdinComment == nil {
			b, err := io.ReadAll(a.in)
			if err != nil {
				return "", fatalf(sevCmdLine, "cannot read comment (%v)", err)
			}
			s := string(b)
			a.stdinComment = &s
		}
		if *a.stdinComment == "" {
			return old, nil
		}
		return *a.stdinComment, nil
	case c.commentFile != "":
		b, err := os.ReadFile(c.commentFile)
		if err != nil {
			return "", fatalf(sevCmdLine, "cannot read comment (%v)", err)
		}
		return string(b), nil
	}
	return a.editComment(old)
}

func (a *app) editComment(old string) (string, error) {
	f, err := os.CreateTemp("", "U$~COMM*.TXT")
	if err != nil {
		return "", fatalf(sevEditor, "cannot create a temporary file (%v)", err)
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.WriteString(old)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	def := "vi"
	if runtime.GOOS == "windows" {
		def = "notepad"
	}
	args := strings.Fields(cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR")))
	if len(args) == 0 {
		args = []string{def}
	}
	if err == nil {
		a.out.Flush()
		e := exec.Command(args[0], append(args[1:], name)...)
		e.Stdin, e.Stdout, e.Stderr = a.rawIn, a.stderr, a.stderr
		err = e.Run()
	}
	var b []byte
	if err == nil {
		b, err = os.ReadFile(name)
	}
	if err != nil {
		return "", fatalf(sevEditor, "cannot edit the comment with %s (%v)", args[0], err)
	}
	return string(b), nil
}
