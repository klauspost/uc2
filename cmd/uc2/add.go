package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/klauspost/uc2"
)

// nameKey folds case: archive names match case-insensitively everywhere, as in D, E and L.
func nameKey(s string) string { return strings.ToLower(s) }

type item struct {
	disk string
	root *os.Root    // the scanned tree; disk files are only accessed through it
	rel  string      // path below root
	name string      // archive name; directories end in "/"
	info fs.FileInfo // nil for directories created for #dest
	old  *uc2.File   // newest archived revision this file replaces
	read fs.FileInfo // the file as it was read, for move mode
}

type adder struct {
	a        *app
	c        *cmd
	excl     []*mask
	pathExcl []*mask // exclusions with a directory part, also matched against disk paths
	self     fs.FileInfo
	newest   map[string]*uc2.File   // name key -> newest revision
	dirs     map[string]string      // name key -> archive directory name
	seen     map[string]fs.FileInfo // name key -> disk file
	items    []*item
	moves    []*item // disk files to delete in move mode
	root     *os.Root
	roots    []*os.Root

	skipped      int64
	skippedBytes int64

	pat     *mask
	prefix  string
	matched bool
}

func (a *app) add(c *cmd, arch string) error {
	a.header("Adding files to", c, arch)
	var r *archive
	self, err := os.Stat(arch)
	switch {
	case err == nil:
		if r, err = a.openArchive(c, arch); err != nil {
			return err
		}
		defer r.Close()
	case !errors.Is(err, fs.ErrNotExist):
		return archiveErr(arch, err)
	case c.freshen:
		return fatalf(sevNoArchive, "%s does not exist", arch)
	}
	ad := &adder{a: a, c: c, excl: compileExcludes(c.excludes), self: self,
		newest: map[string]*uc2.File{}, dirs: map[string]string{"": ""}, seen: map[string]fs.FileInfo{}}
	defer func() {
		for _, r := range ad.roots {
			r.Close()
		}
	}()
	for _, e := range ad.excl {
		if len(e.dirs) > 0 {
			ad.pathExcl = append(ad.pathExcl, e)
		}
	}
	if r != nil {
		for _, f := range r.File {
			if isDir(f) {
				ad.dirs[nameKey(f.Name)] = f.Name
			} else if f.Revision == 0 {
				ad.newest[nameKey(f.Name)] = f
			}
		}
	}
	if !validName(destPrefix(c.dest) + "x") {
		return fatalf(sevCmdLine, "invalid destination %s", c.dest)
	}
	specs := c.specs
	if len(specs) == 0 {
		specs = []string{"*.*"}
	}
	for _, s := range specs {
		ad.scan(s)
	}

	protected := r != nil && r.Protected
	if len(ad.items) == 0 && (r == nil || *c.protection(protected) == protected) {
		ad.report()
		return nil
	}
	if r != nil && c.incremental {
		r.Close()
		err = ad.appendTo(arch)
	} else {
		replaced := map[*uc2.File]bool{}
		for _, it := range ad.items {
			if it.old != nil {
				replaced[it.old] = true
			}
		}
		err = a.rewrite(arch, r, func(f *uc2.File) bool { return replaced[f] },
			func(w *uc2.Writer) error { return ad.write(w, r != nil) }, c.opts(c.protection(protected)))
	}
	if err != nil {
		return err
	}
	ad.report()
	a.contents(c, arch)
	a.moveFiles(ad.moves)
	return nil
}

func (ad *adder) report() {
	if ad.skipped > 0 {
		ad.a.printf("Smart skipped %s (%s)\n", plural(ad.skipped, "file", "files"), plural(ad.skippedBytes, "byte", "bytes"))
	}
}

func destPrefix(dest string) string {
	var p string
	for _, e := range strings.Split(strings.ReplaceAll(dest, `\`, "/"), "/") {
		if e != "" && e != "." {
			p += e + "/"
		}
	}
	return p
}

// scan selects the files of one disk specification. The directory part of
// the specification is not stored; with S, the tree below it keeps its structure.
func (ad *adder) scan(spec string) {
	// DOS style backslashes are separators on every system.
	s := filepath.FromSlash(strings.ReplaceAll(spec, `\`, "/"))
	if strings.HasSuffix(s, string(filepath.Separator)) || strings.HasSuffix(s, "/") {
		s += "*.*"
	}
	base, pat := filepath.Split(s)
	if base == "" {
		base = "."
	}
	ad.pat = newMask(pat)
	ad.prefix = destPrefix(ad.c.dest)
	if ad.c.destSrc {
		for _, e := range strings.Split(filepath.ToSlash(strings.TrimPrefix(base, filepath.VolumeName(base))), "/") {
			if e != "" && e != "." && e != ".." {
				ad.prefix += e + "/"
			}
		}
	}
	ad.matched = false
	var entries []fs.DirEntry
	if root, err := os.OpenRoot(base); err == nil {
		ad.root = root
		ad.roots = append(ad.roots, root)
		entries, _ = ad.readDir(".")
	}
	for _, e := range entries {
		disk := filepath.Join(base, e.Name())
		fi, ok := ad.stat(disk, e.Name())
		if !ok {
			continue
		}
		match := ad.pat.matchName(e.Name(), "")
		switch {
		case fi.IsDir() && ad.c.recurse:
			ad.walk(disk, e.Name(), ad.prefix+e.Name()+"/", fi, match && ad.pat.tree, nil)
		case !fi.IsDir() && match:
			ad.file(disk, e.Name(), ad.prefix+e.Name(), fi, nil)
		}
	}
	if !ad.matched {
		ad.a.warnf(sevNoMatch, "no file found matching %s", spec)
	}
}

// walk scans a directory. With all, everything below is selected and every
// directory is stored; otherwise files must match the pattern and
// directories are stored when they (indirectly) contain a selected file.
func (ad *adder) walk(disk, rel, name string, fi fs.FileInfo, all bool, pending []*item) {
	if ad.excludedPath(name, disk) {
		return
	}
	d := &item{disk: disk, name: name, info: fi}
	if all {
		ad.matched = true
		ad.addDir(d)
	} else {
		pending = append(pending, d)
	}
	entries, err := ad.readDir(rel)
	if err != nil {
		ad.a.warnf(sevSkipped, "skipped directory %s (%v)", disk, err)
		return
	}
	for _, e := range entries {
		p, r := filepath.Join(disk, e.Name()), filepath.Join(rel, e.Name())
		cfi, ok := ad.stat(p, r)
		switch {
		case !ok:
		case cfi.IsDir():
			ad.walk(p, r, name+e.Name()+"/", cfi, all, pending)
		case all || ad.pat.matchName(e.Name(), ""):
			ad.file(p, r, name+e.Name(), cfi, pending)
		}
	}
}

// readDir lists a directory of the scanned tree in name order.
func (ad *adder) readDir(rel string) ([]fs.DirEntry, error) {
	d, err := ad.root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	entries, err := d.ReadDir(-1)
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, err
}

func (ad *adder) stat(p, rel string) (fs.FileInfo, bool) {
	fi, err := ad.root.Lstat(rel)
	if err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		// Directory links are not followed to avoid cycles. Root.Stat
		// refuses file links that lead outside the tree.
		if fi, err = ad.root.Stat(rel); err == nil && fi.IsDir() {
			return nil, false
		}
	}
	if err == nil {
		os.SameFile(fi, fi) // pin the Windows file ID; it is otherwise read by path when compared
	}
	switch {
	case err != nil:
		ad.a.warnf(sevSkipped, "skipped file %s (%v)", p, err)
		return nil, false
	case !fi.IsDir() && !fi.Mode().IsRegular():
		return nil, false
	case ad.self != nil && os.SameFile(fi, ad.self):
		return nil, false
	}
	return fi, true
}

// excludedPath matches exclusions against the path relative to the
// specification. Exclusions with a directory part are also matched against
// the disk path; others would match ancestors of the specification.
func (ad *adder) excludedPath(name, disk string) bool {
	check := excluded
	if strings.HasSuffix(name, "/") {
		check = excludedDir
	}
	rel := strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, ad.prefix), "/"), "/")
	d := strings.Split(strings.TrimLeft(filepath.ToSlash(filepath.Clean(disk)), "/"), "/")
	return check(ad.excl, rel, nil) || check(ad.pathExcl, d, nil)
}

func (ad *adder) file(disk, rel, name string, fi fs.FileInfo, pending []*item) {
	// U$~ files are UC2's temporary files and archive comment; they are only
	// added when named exactly.
	if base := fi.Name(); strings.HasPrefix(strings.ToUpper(base), "U$~") && !strings.EqualFold(base, ad.pat.name) {
		return
	}
	if ad.excludedPath(name, disk) {
		return
	}
	ad.matched = true
	name = ad.canon(name)
	k := nameKey(name)
	if prev, ok := ad.seen[k]; ok {
		if !os.SameFile(prev, fi) {
			ad.a.warnf(sevSkipped, "skipped file %s (conflicting name)", disk)
		}
		return
	}
	ad.seen[k] = fi
	old := ad.newest[k]
	switch {
	case !validName(name) || ad.conflict(name):
		ad.a.warnf(sevSkipped, "skipped file %s (invalid or conflicting name)", disk)
		return
	case ad.c.freshen && old == nil:
		return
	case old != nil && !ad.c.move && sameFile(old, fi.Size(), fi.ModTime()):
		// Move mode stores the file again: a smart skip would delete a file
		// that merely has the size and time of the archived revision.
		ad.skipped++
		ad.skippedBytes += fi.Size()
		ad.a.verbosef("Smart skipping %s\n", disp(name, 0))
		return
	case ad.c.newer && old != nil && dosStamp(fi.ModTime()) <= dosStamp(old.Modified):
		return
	}
	for _, d := range pending {
		ad.addDir(d)
	}
	ad.ensureParents(name)
	ad.items = append(ad.items, &item{disk: disk, root: ad.root, rel: rel, name: name, info: fi, old: old})
}

// open opens a scanned file, which must still be the regular file the scan
// found: it may have been swapped for a FIFO, a device or a link since.
func (it *item) open() (*os.File, fs.FileInfo, error) {
	f, err := it.root.OpenFile(it.rel, os.O_RDONLY|oNonblock, 0)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err == nil && (!fi.Mode().IsRegular() || !os.SameFile(fi, it.info)) {
		err = errors.New("file was replaced")
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

func (ad *adder) addDir(d *item) {
	d.name = ad.canon(d.name)
	k := nameKey(d.name)
	if _, ok := ad.dirs[k]; ok {
		return
	}
	if !validName(d.name) || ad.conflict(d.name) {
		ad.a.warnf(sevSkipped, "skipped directory %s (invalid or conflicting name)", d.disk)
		ad.dirs[k] = d.name
		return
	}
	ad.ensureParents(d.name)
	ad.dirs[k] = d.name
	ad.items = append(ad.items, d)
}

// ensureParents adds the missing parent directories of name, which exist
// only for #dest.
func (ad *adder) ensureParents(name string) {
	for i := 0; i < len(name)-1; i++ {
		if name[i] != '/' {
			continue
		}
		if p := name[:i+1]; ad.dirs[nameKey(p)] == "" {
			ad.dirs[nameKey(p)] = p
			ad.items = append(ad.items, &item{name: p})
		}
	}
}

// canon returns name with the spelling of existing archive entries.
func (ad *adder) canon(name string) string {
	out := ""
	for {
		i := strings.IndexByte(name, '/')
		if i < 0 {
			break
		}
		p := out + name[:i+1]
		if d, ok := ad.dirs[nameKey(p)]; ok && d != "" {
			p = d
		}
		out, name = p, name[i+1:]
	}
	if name == "" {
		return out
	}
	if f := ad.newest[nameKey(out+name)]; f != nil {
		return f.Name
	}
	return out + name
}

// conflict reports whether name clashes with an archived file or directory.
func (ad *adder) conflict(name string) bool {
	t := strings.TrimSuffix(name, "/")
	for i := range len(t) {
		if t[i] == '/' && ad.newest[nameKey(t[:i])] != nil {
			return true
		}
	}
	if t != name {
		return ad.newest[nameKey(t)] != nil
	}
	_, dir := ad.dirs[nameKey(name+"/")]
	return dir
}

// validName reports whether the library accepts name as an archive path.
func validName(name string) bool {
	for _, e := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if e == "" || e == "." || e == ".." || len(e) > 255 || strings.ContainsAny(e, "\\\x00") || !utf8.ValidString(e) {
			return false
		}
	}
	return true
}

// write adds the selected items. If a file cannot be opened, restore keeps
// the archived revision it would have replaced.
func (ad *adder) write(w *uc2.Writer, restore bool) error {
	for _, it := range ad.items {
		if strings.HasSuffix(it.name, "/") {
			fh := &uc2.FileHeader{Name: it.name, Modified: time.Now(), Attr: uc2.AttrDir}
			if it.info != nil {
				fh, _ = uc2.FileInfoHeader(it.info)
				fh.Name = it.name
			}
			if _, err := w.CreateHeader(fh); err != nil {
				return err
			}
			continue
		}
		f, fi, err := it.open()
		if err != nil {
			ad.a.warnf(sevSkipped, "skipped file %s (%v)", it.disk, err)
			if it.old != nil && restore {
				if err := w.Copy(it.old); err != nil {
					return err
				}
			}
			continue
		}
		fh, _ := uc2.FileInfoHeader(fi)
		fh.Name = it.name
		if it.old != nil {
			fh.ShortName = it.old.ShortName
		}
		ew, err := w.CreateHeader(fh)
		var n int64
		if err == nil {
			n, err = io.Copy(ew, io.LimitReader(f, fi.Size()))
		}
		f.Close()
		if err != nil {
			return fmt.Errorf("adding %s: %w", it.disk, err)
		}
		ad.a.say("Compressing %s DONE", "Add %s", disp(it.name, 0))
		if ad.c.move && n == fi.Size() {
			it.read = fi
			ad.moves = append(ad.moves, it)
		}
	}
	return nil
}

// appendTo adds the items in place, keeping all revisions (incremental mode).
func (ad *adder) appendTo(arch string) error {
	f, err := os.OpenFile(arch, os.O_RDWR, 0)
	if err != nil {
		return fatalf(sevWrite, "cannot open %s for writing (%v)", arch, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return writeErr(arch, err)
	}
	var prot *bool
	if ad.c.protect || ad.c.unprotect {
		prot = ad.c.protection(false)
	}
	w, err := uc2.NewAppendWriter(f, ad.c.opts(prot)...)
	if err != nil {
		return appendErr(arch, err)
	}
	if err := ad.write(w, false); err != nil {
		// Closing would commit the partial update; the appended data is unreferenced.
		f.Truncate(fi.Size())
		return writeErr(arch, err)
	}
	// Close rolls back itself if it fails before committing the new header.
	if err := w.Close(); err != nil {
		return writeErr(arch, err)
	}
	return nil
}

// moveFiles deletes the added files, but only if they are unchanged since
// they were read.
func (a *app) moveFiles(files []*item) {
	if len(files) == 0 {
		return
	}
	a.printf("Moving files\n")
	for _, it := range files {
		cur, err := it.root.Lstat(it.rel)
		if err == nil && cur.Mode()&fs.ModeSymlink != 0 {
			cur, err = it.root.Stat(it.rel) // deleting a link loses no data
		}
		if err == nil && !unchanged(cur, it.read) {
			err = errors.New("file changed after it was added")
		}
		if err == nil {
			err = it.root.Remove(it.rel)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			a.errorf(sevDelete, "failed to delete %s (%v)", it.disk, err)
		}
	}
}

// contents prints the totals of an updated archive.
func (a *app) contents(c *cmd, arch string) {
	r, err := uc2.OpenReader(arch, c.opts(nil)...)
	if err != nil {
		return
	}
	defer r.Close()
	var files, dirs, size int64
	for _, f := range r.File {
		if isDir(f) {
			dirs++
		} else {
			files++
			size += f.Size
		}
	}
	s := "is empty"
	if files+dirs > 0 {
		s = "contains"
		if files > 0 {
			s += " " + plural(files, "file", "files") + " (" + plural(size, "byte", "bytes") + ")"
		}
		if files > 0 && dirs > 0 {
			s += " and"
		}
		if dirs > 0 {
			s += " " + plural(dirs, "directory", "directories")
		}
	}
	a.printf("Updated archive %s\n", s)
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return neat(n) + " " + many
}
