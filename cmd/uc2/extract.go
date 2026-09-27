package main

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"
	"runtime"
	"slices"
	"strings"

	"github.com/klauspost/uc2"
	"github.com/klauspost/uc2/internal/safename"
)

func (a *app) extract(c *cmd, arch string) error {
	a.header("Extracting files from", c, arch)
	r, err := a.openArchive(c, arch)
	if err != nil {
		return err
	}
	defer r.Close()
	if c.move {
		if err := r.check(arch); err != nil {
			return err
		}
	}
	masks := compileMasks(c.specs, "*.*")
	sel := newTree(&r.Reader).selectFiles(c, masks, compileExcludes(c.excludes), 0, true)
	if len(c.specs) > 0 {
		a.warnUnmatched(masks)
	}
	if len(sel) == 0 {
		return nil
	}
	dest := cmp.Or(c.dest, ".")
	if err := os.MkdirAll(dest, 0o777); err != nil {
		return fatalf(sevWrite, "cannot create directory %s (%v)", dest, err)
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return fatalf(sevWrite, "cannot open directory %s (%v)", dest, err)
	}
	defer root.Close()
	// Several selected revisions can end up at the same disk file; only the
	// last one written is on disk.
	type onDisk struct {
		target string
		f      *uc2.File
		fi     fs.FileInfo // the file right after writing it
	}
	done := map[string]onDisk{}
	git := &gitGuard{root: root}
	for _, s := range sel {
		target, ok := a.diskPath(s.rel)
		if !ok {
			a.errorf(sevWrite, "cannot write to %s, file skipped (invalid name)", disp(s.rel, 0))
			continue
		}
		fresh, ok := git.check(target)
		if !ok {
			a.warnf(sevSkipped, "skipping %s (refusing to write into existing .git directory)", disp(target, 0))
			continue
		}
		_, again := done[nameKey(target)]
		ok, err := a.extractFile(c, root, s, target, again)
		git.created(fresh)
		if err != nil {
			return err
		}
		if ok {
			fi, _ := root.Lstat(target)
			done[nameKey(target)] = onDisk{target, s.f, fi}
		}
	}
	if !c.move || len(done) == 0 {
		return nil
	}
	// Move mode removes the revisions that are now on disk. Checking the
	// disk again guards against files replaced through other names, such
	// as Windows 8.3 aliases.
	drop := map[*uc2.File]bool{}
	for _, d := range done {
		if fi, err := root.Lstat(d.target); err == nil && os.SameFile(fi, d.fi) && fi.Mode().IsRegular() && sameFile(d.f, fi.Size(), fi.ModTime()) {
			drop[d.f] = true
		}
	}
	return a.rewrite(arch, r, func(f *uc2.File) bool { return drop[f] }, nil, c.opts(c.protection(r.Protected)))
}

// diskPath returns the path below the destination for an archive path.
// Invalid names are rejected even though the library sanitizes them, so a
// path can never leave the destination. So are other names of .git, such
// as its NTFS short name GIT~1, which bypass the .git check.
func (a *app) diskPath(rel string) (string, bool) {
	if !validName(rel) || slices.ContainsFunc(strings.Split(rel, "/"), safename.Alias) {
		return "", false
	}
	if runtime.GOOS == "windows" {
		m := mapPath(rel)
		from, to := strings.Split(rel, "/"), strings.Split(m, "/")
		for i := range from {
			if k := strings.Join(from[:i+1], "/"); from[i] != to[i] && !a.mapped[k] {
				a.mapped[k] = true
				a.warnf(sevMapped, "mapped (device)name %s to %s", disp(k, 0), disp(strings.Join(to[:i+1], "/"), 0))
			}
		}
		rel = m
	}
	return strings.TrimSuffix(rel, "/"), true
}

// gitGuard allows writing below a .git element only if this run created
// it: restoring a project backup works, but files cannot be planted into
// an existing repository (hooks, config).
type gitGuard struct {
	root *os.Root
	made []fs.FileInfo
}

// check reports whether target may be written, and returns its .git paths
// that do not exist yet.
func (g *gitGuard) check(target string) (fresh []string, ok bool) {
	elems := strings.Split(target, "/")
	for i, e := range elems {
		if e != ".git" {
			continue
		}
		p := strings.Join(elems[:i+1], "/")
		fi, err := g.root.Lstat(p)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			fresh = append(fresh, p)
		case err != nil || !slices.ContainsFunc(g.made, func(m fs.FileInfo) bool { return os.SameFile(m, fi) }):
			return nil, false
		}
	}
	return fresh, true
}

// created records the paths returned by check that exist now.
func (g *gitGuard) created(fresh []string) {
	for _, p := range fresh {
		if fi, err := g.root.Lstat(p); err == nil {
			g.made = append(g.made, fi)
		}
	}
}

// extractFile writes one revision to target below root. again is set if
// another revision was written there before, so that smart skipping would
// compare against it. It reports whether the revision is on disk
// afterwards; errors are fatal.
func (a *app) extractFile(c *cmd, root *os.Root, s selected, target string, again bool) (bool, error) {
	if isDir(s.f) {
		if err := root.MkdirAll(target, 0o777); err != nil {
			a.errorf(sevWrite, "cannot create directory %s (%v)", disp(target, 0), err)
		}
		return false, nil
	}
	if dir := path.Dir(target); dir != "." {
		if err := root.MkdirAll(dir, 0o777); err != nil {
			a.errorf(sevWrite, "cannot write to %s, file skipped (%v)", disp(target, 0), err)
			return false, nil
		}
	}
	name := dispFile(s.f)
	fi, err := root.Lstat(target)
	exists := err == nil
	if exists {
		switch {
		case fi.IsDir():
			a.errorf(sevWrite, "cannot write to %s, file skipped (it is a directory)", disp(target, 0))
			return false, nil
		case !again && fi.Mode().IsRegular() && sameFile(s.f, fi.Size(), fi.ModTime()) && (!c.move || sameContent(root, target, s.f)):
			// Move mode drops the revision from the archive, so the disk
			// file must really hold it.
			a.say("Smart skipping %s", "", name)
			return true, nil
		case c.newer && dosStamp(s.f.Modified) <= dosStamp(fi.ModTime()):
			return false, nil
		}
		ok, err := a.confirmOverwrite(c, disp(target, 0))
		if !ok || err != nil {
			return false, err
		}
	}
	rc, err := s.f.Open()
	if err != nil {
		return false, a.damagedFile(s.f, err)
	}
	defer rc.Close()
	// Decompress next to the target first, so a damaged revision never
	// replaces an existing file.
	tmp := path.Join(path.Dir(target), fmt.Sprintf("U$~%05d.TMP", rand.IntN(100000)))
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		a.errorf(sevWrite, "cannot write to %s, file skipped (%v)", disp(target, 0), err)
		return false, nil
	}
	defer track(out, func() error { return root.Remove(tmp) })()
	_, err = io.Copy(out, rc)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Chtimes(tmp, s.f.Modified, s.f.Modified)
	}
	switch {
	case err != nil:
	case exists && runtime.GOOS == "windows" && fi.Mode().IsRegular() && fi.Mode()&0o200 == 0:
		err = replaceReadOnly(root, tmp, target)
	default:
		// Rename atomically replaces the target itself, never what a symlink points to.
		err = root.Rename(tmp, target)
	}
	if err != nil {
		root.Remove(tmp)
		if damaged(err) || errors.Is(err, errors.ErrUnsupported) {
			return false, a.damagedFile(s.f, err)
		}
		a.errorf(sevWrite, "cannot write to %s, file skipped (%v)", disp(target, 0), err)
		return false, nil
	}
	if s.f.Attr&uc2.AttrReadOnly != 0 {
		root.Chmod(target, 0o444)
	}
	a.say("Decompressing %s OK", "Extract %s", name)
	return true, nil
}

// replaceReadOnly replaces a read-only file on Windows, where a rename
// cannot. Clearing the attribute first would clear it for every hard link
// of the file, and leave it cleared if the rename fails. Remove ignores it.
func replaceReadOnly(root *os.Root, tmp, target string) error {
	old := tmp + "~"
	if err := root.Rename(target, old); err != nil {
		return err
	}
	if err := root.Rename(tmp, target); err != nil {
		root.Rename(old, target)
		return err
	}
	root.Remove(old)
	return nil
}

// sameContent reports whether the file target holds the data of f.
func sameContent(root *os.Root, target string, f *uc2.File) bool {
	df, err := root.Open(target)
	if err != nil {
		return false
	}
	defer df.Close()
	rc, err := f.Open()
	if err != nil {
		return false
	}
	defer rc.Close()
	eof := func(err error) bool { return err == io.EOF || err == io.ErrUnexpectedEOF }
	b1, b2 := make([]byte, 32<<10), make([]byte, 32<<10)
	for {
		n1, err1 := io.ReadFull(df, b1)
		n2, err2 := io.ReadFull(rc, b2)
		if n1 != n2 || !bytes.Equal(b1[:n1], b2[:n2]) {
			return false
		}
		if err1 != nil || err2 != nil {
			return eof(err1) && eof(err2)
		}
	}
}

// damagedFile reports a revision that cannot be decompressed. Only
// unsupported features are fatal.
func (a *app) damagedFile(f *uc2.File, err error) error {
	if errors.Is(err, errors.ErrUnsupported) {
		return fatalf(sevVersion, "%s: %v", dispFile(f), err)
	}
	a.errorf(sevDamaged, "file %s is damaged", dispFile(f))
	return nil
}

func (a *app) confirmOverwrite(c *cmd, name string) (bool, error) {
	switch {
	case c.force || a.overwrite == overwriteAll:
		return true, nil
	case a.overwrite == overwriteNone:
		a.printf("Skipping %s (already exists)\n", name)
		return false, nil
	case a.tty:
		choice, err := a.ask("Overwrite file "+name+" ?", []string{"Yes", "No", "Always overwrite files", "nEver overwrite files"}, "YNAE")
		if err != nil || choice >= 0 {
			return a.overwriteChoice(choice, name), err
		}
		a.tty = false // end of input, e.g. from NUL, which is a character device
	}
	a.warnf(sevSkipped, "skipping %s (already exists)", name)
	return false, nil
}

func (a *app) overwriteChoice(choice int, name string) bool {
	switch choice {
	case 2:
		a.overwrite = overwriteAll
	case 3:
		a.overwrite = overwriteNone
	}
	if choice == 1 || choice == 3 {
		a.printf("Skipping %s (already exists)\n", name)
		return false
	}
	return true
}

// ask shows a UC2 style menu on stderr and returns the chosen option, or -1
// at the end of the input.
func (a *app) ask(question string, options []string, keys string) (int, error) {
	a.out.Flush()
	fmt.Fprintf(a.stderr, "\n%s\n", clean(question))
	for i, o := range options {
		fmt.Fprintf(a.stderr, "   %d -> %s\n", i+1, o)
	}
	for {
		fmt.Fprint(a.stderr, "CHOICE (+=Abort) ? ")
		line, err := a.in.ReadString('\n')
		s := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case s == "+":
			return 0, fatalf(sevAbort, "program aborted by user")
		case err != nil && s == "":
			fmt.Fprintln(a.stderr)
			return -1, nil
		case len(s) != 1:
		case strings.IndexByte(keys, s[0]) >= 0:
			return strings.IndexByte(keys, s[0]), nil
		case s[0] >= '1' && int(s[0]-'1') < len(options):
			return int(s[0] - '1'), nil
		}
	}
}

// Windows device names, reserved with any extension.
var deviceNames = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true, "CLOCK$": true, "CONIN$": true, "CONOUT$": true}

func init() {
	for _, i := range "123456789¹²³" {
		deviceNames["COM"+string(i)] = true
		deviceNames["LPT"+string(i)] = true
	}
}

// mapPath makes each element of a slash separated path valid on Windows:
// device names get a '_' prefix, invalid characters (including ':', which
// selects alternate data streams) and trailing dots and spaces become '_'.
func mapPath(p string) string {
	dir := strings.HasSuffix(p, "/")
	elems := strings.Split(strings.TrimSuffix(p, "/"), "/")
	for i, e := range elems {
		b := []rune(e)
		for j, r := range b {
			if r < 0x20 || strings.ContainsRune(`\:*?"<>|`, r) {
				b[j] = '_'
			}
		}
		for j := len(b) - 1; j >= 0 && (b[j] == '.' || b[j] == ' '); j-- {
			b[j] = '_'
		}
		e = string(b)
		base, _, _ := strings.Cut(e, ".")
		if deviceNames[strings.ToUpper(strings.TrimRight(base, " "))] {
			e = "_" + e
		}
		elems[i] = e
	}
	p = strings.Join(elems, "/")
	if dir {
		p += "/"
	}
	return p
}
