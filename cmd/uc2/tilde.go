package main

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/uc2"
	"github.com/klauspost/uc2/internal/format"
	"github.com/klauspost/uc2/internal/tags"
)

// Tags that the library manages, which ~R keeps.
const (
	tagLongName = "AIP:Win95 LongN"
	tagUTF8Name = "UC2X:UTF8Name"
	tagSize64   = "UC2X:Size64"
)

const resultFile = "U$~RESLT.OK"

// isDumpCommand reports whether word is a ~ command that ends like UC2's
// dump mode.
func isDumpCommand(word string) bool {
	return len(word) > 1 && strings.IndexByte("DXRKVM", upper(word[1])) >= 0
}

// tilde runs UC2's hidden commands for front ends (COMOTERP.CPP:1620-1740).
// All but ~~ switch to minimal output, and end without "Everything went
// OK" but with U$~RESLT.OK. ~M (a key to the exit code) and ~~ (store a
// serial number) do nothing.
func (a *app) tilde(c *cmd) error {
	if c.tilde == '~' {
		return nil
	}
	a.dump, a.verbosity = true, quiet
	switch c.tilde {
	case 'D':
		for _, arch := range a.archives(c) {
			if err := a.dumpList(c, arch); err != nil {
				return err
			}
		}
	case 'X':
		return a.dumpTags(c, c.specs[0], c.specs[1])
	case 'R':
		return a.readTags(c, c.specs[0], c.specs[1])
	case 'K':
		return a.killPath(c.specs[0])
	case 'V':
		return a.viewFile(c.specs[0])
	}
	return nil
}

// dumpResult creates U$~RESLT.OK in the current directory when a run with
// dump commands succeeded, unless UC2_OK is OFF, and removes it after
// errors and warnings (MAIN.CPP exito).
func dumpResult(ok bool) {
	switch {
	case !ok:
		os.Remove(resultFile)
	case !strings.EqualFold(os.Getenv("UC2_OK"), "OFF"):
		os.Remove(resultFile) // and create it anew: never write through a planted link
		if f, err := os.OpenFile(resultFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666); err == nil {
			f.Close()
		}
	}
}

// ctree is UC2's tree of an archive (SUPERMAN.CPP), which ~D, ~X and ~R
// walk. Names compare in UC2's 11 character form, decoded with cs.
type ctree struct {
	cs    uc2.Charset
	root  *node
	dirs  map[nkey]*node // the first subdirectory of each name, in UC2's order
	files map[nkey]int   // the group of files of each name
}

// nkey is a name in a directory.
type nkey struct {
	d    *node
	name [11]rune
}

// node is a directory: its subdirectories in reverse archive order, as UC2
// inserts each at the front, and its files in order of first appearance,
// each with its revisions newest first.
type node struct {
	f      *uc2.File // nil for the root
	dirs   []*node
	groups [][]*uc2.File
}

func newCTree(r *uc2.Reader, c *cmd) *ctree {
	t := &ctree{cs: cmp.Or(c.charset, uc2.CP437), root: &node{}, dirs: map[nkey]*node{}, files: map[nkey]int{}}
	dirs, all := map[uint32]*node{0: t.root}, []*node{t.root}
	for _, f := range r.File {
		e := tags.Record(f)
		p := dirs[e.Meta.Parent]
		if e.Type == format.BoDir {
			d := &node{f: f}
			p.dirs = append(p.dirs, d)
			all = append(all, d)
			if _, dup := dirs[e.Index]; !dup && e.Index != 0 {
				dirs[e.Index] = d // the first one wins, as in the reader
			}
			continue
		}
		k := nkey{p, t.fcbOf(f)}
		i, ok := t.files[k]
		if !ok {
			i, t.files[k] = len(p.groups), len(p.groups)
			p.groups = append(p.groups, nil)
		}
		p.groups[i] = append(p.groups[i], f)
	}
	for _, n := range all {
		slices.Reverse(n.dirs)
		for _, s := range n.dirs {
			if k := (nkey{n, t.fcbOf(s.f)}); t.dirs[k] == nil {
				t.dirs[k] = s
			}
		}
		for _, g := range n.groups {
			slices.Reverse(g)
		}
	}
	return t
}

func (t *ctree) oem(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(t.cs.DecodeByte(c))
	}
	return sb.String()
}

func (t *ctree) name(f *uc2.File) string { return t.oem(tags.Record(f).Meta.Name.Bytes()) }

// dumpName is the name ~D shows. Front ends such as Total Commander pass the
// names back unquoted in scripts, so a leading & @ # or ! would split the
// command, read a script, set the destination or exclude files. The wildcard
// ? still selects the entry.
func (t *ctree) dumpName(f *uc2.File) string {
	n := t.name(f)
	if n != "" && strings.IndexByte("&@#!", n[0]) >= 0 {
		n = "?" + n[1:]
	}
	return n
}

func (t *ctree) fcbOf(f *uc2.File) (n [11]rune) {
	for i, b := range tags.Record(f).Meta.Name {
		n[i] = t.cs.DecodeByte(b)
	}
	return n
}

// fcb converts a name to UC2's 11 character form (Name2Rep, Exp): the base
// cut to 8 and the extension to 3 characters, padded with blanks, where
// '*' fills the rest of its field with '?'.
func fcb(s string) (n [11]rune) {
	base, ext := s, ""
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		base, ext = s[:i], s[i+1:]
	}
	fill := func(dst []rune, s string) {
		r, star := []rune(s), false
		for i := range dst {
			star = star || i < len(r) && r[i] == '*'
			switch {
			case star:
				dst[i] = '?'
			case i < len(r):
				dst[i] = r[i]
			default:
				dst[i] = ' '
			}
		}
	}
	fill(n[:8], base)
	fill(n[8:], ext)
	return n
}

func isSep(r rune) bool { return r == '\\' || r == '/' }

// dir returns the directory named by the elements of p, each ASCII upper
// case (SUPERMAN.CPP TCWI), and its 8.3 path, or nil.
func (t *ctree) dir(p string) (*node, string) {
	d, dpath := t.root, ""
	for _, el := range strings.FieldsFunc(p, isSep) {
		if d = t.dirs[nkey{d, fcb(asciiUpper(el))}]; d == nil {
			return nil, ""
		}
		dpath += t.name(d.f) + `\`
	}
	return d, dpath
}

// entry resolves a path of a tag dump like UC2's AccessRev: the directories
// as for dir, the file by its exact name and ";N" for its revision N. A
// path ending in a separator names a directory, which UC2 cannot update.
func (t *ctree) entry(p string) *uc2.File {
	rev := 0
	if i := strings.LastIndexByte(p, ';'); i > 0 {
		p, rev = p[:i], atoi(p[i+1:])
	}
	i := strings.LastIndexFunc(p, isSep)
	d, _ := t.dir(p[:i+1])
	switch {
	case d == nil:
		return nil
	case i == len(p)-1:
		return d.f
	}
	g, ok := t.files[nkey{d, fcb(p[i+1:])}]
	if !ok || rev < 0 || rev >= len(d.groups[g]) {
		return nil
	}
	return d.groups[g][rev]
}

// walk calls fn for d and all directories below it.
func (d *node) walk(fn func(*node) error) error {
	if err := fn(d); err != nil {
		return err
	}
	for _, s := range d.dirs {
		if err := s.walk(fn); err != nil {
			return err
		}
	}
	return nil
}

// tmask is a mask of ~D in UC2's form (COMOTERP.CPP Anal): 11 characters,
// of which '?' matches any, and a revision.
type tmask struct {
	fcb [11]rune
	all bool
	rev int
}

// newTMask splits a mask into its directory and a tmask. Without ";" it
// selects the newest revision, with ";N" revision N and with a '*' after
// the ";" all of them.
func newTMask(s string) (string, tmask) {
	var m tmask
	if strings.HasSuffix(s, `\`) || strings.HasSuffix(s, "/") {
		s += "*.*"
	}
	if i := strings.LastIndexByte(s, ';'); i >= 0 {
		m.all, m.rev, s = strings.LastIndexByte(s, '*') > i, atoi(s[i+1:]), s[:i]
	}
	i := strings.LastIndexFunc(s, isSep)
	m.fcb = fcb(asciiUpper(s[i+1:]))
	return s[:i+1], m
}

func (m tmask) fit(n [11]rune, rev int) bool {
	return fcbMatch(m.fcb, n) && (m.all || m.rev == rev)
}

// exact reports whether m names a single revision of a U$~ file, to which
// the exclusions do not apply (SUPERMAN.CPP iLastNoQ).
func (m tmask) exact() bool {
	return !m.all && string(m.fcb[:3]) == "U$~" && !slices.Contains(m.fcb[:], '?')
}

// mgroup holds the masks of one directory ("MPATH").
type mgroup struct {
	dir   string
	masks []tmask
	d     *node
	dpath string
}

func (g *mgroup) selects(n [11]rune, rev int, excl []tmask) bool {
	ok := false
	for _, m := range g.masks {
		if m.fit(n, rev) {
			if m.exact() {
				return true
			}
			ok = true
		}
	}
	return ok && !slices.ContainsFunc(excl, func(m tmask) bool { return m.fit(n, rev) })
}

// dumpList implements ~D, the listing for front ends (LIST.CPP bDump). It
// is recursive; without masks it lists all revisions, U$~ files included.
func (a *app) dumpList(c *cmd, arch string) error {
	r, err := a.openArchive(c, arch)
	if err != nil {
		return err
	}
	defer r.Close()
	t := newCTree(&r.Reader, c)
	_, all := newTMask("*.*;*")
	groups := []*mgroup{{masks: []tmask{all}}}
	var excl []tmask
	if len(c.specs)+len(c.excludes) > 0 {
		groups = nil
		_, u := newTMask("U$~?????.???")
		excl = []tmask{u}
		for _, s := range c.excludes {
			_, m := newTMask(s) // the directory does not matter, as in UC2
			excl = append(excl, m)
		}
		specs := c.specs
		if len(specs) == 0 {
			specs = []string{"*.*"}
		}
		for _, s := range specs {
			dir, m := newTMask(s)
			dir = asciiUpper(strings.Join(strings.FieldsFunc(dir, isSep), `\`))
			if i := slices.IndexFunc(groups, func(g *mgroup) bool { return g.dir == dir }); i >= 0 {
				groups[i].masks = append(groups[i].masks, m)
			} else {
				groups = append([]*mgroup{{dir: dir, masks: []tmask{m}}}, groups...) // UC2 prepends
			}
		}
	}
	// Everything is selected before anything is listed, as in UC2.
	marked := map[*uc2.File]bool{}
	for _, g := range groups {
		if g.d, g.dpath = t.dir(g.dir); g.d == nil {
			continue
		}
		err := g.d.walk(func(n *node) error {
			for _, revs := range n.groups {
				for i, f := range revs {
					switch {
					case !g.selects(t.fcbOf(f), i, excl):
					case marked[f]:
						return fatalf(sevCmdLine, "double reference to single file, please simplify command line")
					default:
						marked[f] = true
					}
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for _, g := range groups {
		if g.d == nil {
			continue
		}
		a.dumpf("LIST [\\]\n")
		a.dumpDir(t, g.d, marked)
		a.dumpBelow(t, g.d, g.dpath, marked)
	}
	cd := tags.CDIR(&r.Reader)
	prot := 0
	if r.Protected {
		prot = 1
	}
	a.dumpf("END\nSPEC-SECTION\n    DAMPRO = %d\n", prot)
	if l := cd.Tail.Label; l[0] != 0 {
		lb, _, _ := bytes.Cut(l[:], []byte{0})
		a.dumpf("    VOLLABEL = \"%s\"\n", t.oem(lb))
	}
	rev := r.MadeBy % 100
	if rev == 0 {
		rev = 1
	}
	a.dumpf("    SERIAL = %s\n    UC2-REVISION = %d\nEND\n", neat(int64(cd.Serial)), rev)
	return nil
}

// dumpBelow lists the directories below d, whose 8.3 path is dpath.
func (a *app) dumpBelow(t *ctree, d *node, dpath string, marked map[*uc2.File]bool) {
	for _, s := range d.dirs {
		p := dpath + t.dumpName(s.f) + `\`
		a.dumpf("LIST [\\%s]\n", p)
		a.dumpDir(t, s, marked)
		a.dumpBelow(t, s, p, marked)
	}
}

// dumpDir lists the subdirectories of d and its selected file revisions.
func (a *app) dumpDir(t *ctree, d *node, marked map[*uc2.File]bool) {
	for _, s := range d.dirs {
		a.dumpEntry(t, s.f, -1)
	}
	for _, revs := range d.groups {
		for i, f := range revs {
			if marked[f] {
				a.dumpEntry(t, f, i)
			}
		}
	}
}

// dumpEntry lists a directory, or revision rev of a file. As UC 2.37b, it
// shows the long name of entries with a Win95 long name tag.
func (a *app) dumpEntry(t *ctree, f *uc2.File, rev int) {
	e := tags.Record(f)
	kind := "FILE"
	if rev < 0 {
		kind = "DIR"
	}
	a.dumpf("   %s\n      NAME=[%s]\n", kind, t.dumpName(f))
	if ln, ok := tagData(e.Tags, tagLongName); ok {
		ln, _, _ = bytes.Cut(ln, []byte{0})
		s := t.oem(ln)
		if u, _ := tagData(e.Tags, tagUTF8Name); len(u) > 0 && utf8.Valid(u) {
			s = string(u)
		}
		a.dumpf("      LONGNAME=[%s]\n", s)
	}
	if rev >= 0 {
		w := uint32(e.Fletch)
		a.dumpf("      VERSION=%d\n      SIZE=%d\n      CHECK=%04X%08X\n", rev, f.Size, w, e.Size^0x26F79FA4^(w|w<<16))
	}
	m := e.Meta
	var attr []byte
	for _, x := range []struct {
		bit uint8
		c   byte
	}{{0x20, 'A'}, {0x01, 'R'}, {0x04, 'S'}, {0x02, 'H'}} {
		if m.Attr&x.bit != 0 {
			attr = append(attr, x.c)
		}
	}
	a.dumpf("      DATE(MDY)=%02d %02d %04d\n      TIME(HMS)=%02d %02d %02d\n      ATTRIB=%s\n",
		m.Date>>5&15, m.Date&31, int(m.Date>>9)+1980, m.Time>>11, m.Time>>5&63, (m.Time&31)*2, string(attr))
	for i := len(e.Tags) - 1; i >= 0; i-- {
		a.dumpf("      TAG=[%s]\n", strings.TrimPrefix(t.oem([]byte(e.Tags[i].Name)), "AIP:"))
	}
}

func tagData(l []format.Tag, name string) ([]byte, bool) {
	for _, x := range l {
		if x.Name == name {
			return x.Data, true
		}
	}
	return nil, false
}

// dumpTags implements ~X: it writes the tags of all entries to dump
// (TAGMAN.CPP). The archive name is used as it is, as in UC2.
func (a *app) dumpTags(c *cmd, arch, dump string) error {
	r, err := a.openArchive(c, arch)
	if err != nil {
		return err
	}
	defer r.Close()
	if fi, err := os.Stat(dump); err == nil && os.SameFile(fi, r.info) {
		return fatalf(sevCmdLine, "the dumpfile %s is the archive", dump)
	}
	b, err := tagDump(newCTree(&r.Reader, c))
	if err == nil {
		err = os.WriteFile(dump, b, 0o666)
	}
	if err != nil {
		return fatalf(sevWrite, "cannot write %s (%v)", dump, err)
	}
	return nil
}

// tagDump returns the tags in UC2's dump format: a record per entry with
// its 8.3 path, a length whose bit 15 marks files, and for entries with
// tags an operation to delete them all and one to add each, in UC2's
// reverse order. Root files come first, then each directory with its
// files and subdirectories.
func tagDump(t *ctree) ([]byte, error) {
	var b []byte
	var err error
	rec := func(p []byte, file bool, tg []format.Tag) {
		if len(p) >= 0x7FFF {
			err = fmt.Errorf("path %s is too long", t.oem(p))
			return
		}
		l := uint16(len(p) + 1)
		if file {
			l |= 0x8000
		}
		b = binary.LittleEndian.AppendUint16(b, l)
		b = append(append(b, p...), 0)
		if len(tg) > 0 {
			b = append(b, 1)
		}
		for i := len(tg) - 1; i >= 0; i-- {
			var name [16]byte // UC2 left stack garbage after the NUL
			copy(name[:format.TagNameMax], tg[i].Name)
			b = append(append(b, 2), name[:]...)
			b = binary.LittleEndian.AppendUint32(b, uint32(len(tg[i].Data)))
			b = append(b, tg[i].Data...)
		}
		b = append(b, 0)
	}
	files := func(d *node, p []byte) {
		for _, revs := range d.groups {
			for i, f := range revs {
				e := tags.Record(f)
				fp := append(slices.Clip(p), e.Meta.Name.Bytes()...)
				if i > 0 {
					fp = fmt.Appendf(fp, ";%d", i)
				}
				rec(fp, true, e.Tags)
			}
		}
	}
	var dirs func(d *node, p []byte)
	dirs = func(d *node, p []byte) {
		for _, s := range d.dirs {
			e := tags.Record(s.f)
			sp := append(append(slices.Clip(p), e.Meta.Name.Bytes()...), '\\')
			rec(sp, false, e.Tags)
			files(s, sp)
			dirs(s, sp)
		}
	}
	files(t.root, nil)
	dirs(t.root, nil)
	return append(b, 0, 0), err
}

// dumpRec is a record of a tag dump: a path and its operations, where a
// nil tag deletes all tags.
type dumpRec struct {
	path []byte
	ops  []*format.Tag
}

// parseDump parses a tag dump. Like UC2, it takes the end of the data
// where a record would start as the end, and ignores what follows the
// terminator.
func parseDump(b []byte) ([]dumpRec, error) {
	var recs []dumpRec
	i := 0
	bad := func(why string) error { return fmt.Errorf("%s at offset %d", why, i) }
	for i < len(b) {
		if len(b)-i < 2 {
			return nil, bad("truncated record")
		}
		l := binary.LittleEndian.Uint16(b[i:])
		if l == 0 {
			break
		}
		n := int(l & 0x7FFF)
		i += 2
		if len(b)-i < n {
			return nil, bad("truncated path")
		}
		p, _, ok := bytes.Cut(b[i:i+n], []byte{0})
		if !ok {
			return nil, bad("path without NUL")
		}
		i += n
		r := dumpRec{path: p}
		for op := byte(1); op != 0; {
			if i >= len(b) {
				return nil, bad("truncated record")
			}
			switch op = b[i]; op {
			case 0:
				i++
			case 1:
				r.ops = append(r.ops, nil)
				i++
			case 2:
				if len(b)-i < 21 {
					return nil, bad("truncated tag")
				}
				name, _, ok := bytes.Cut(b[i+1:i+17], []byte{0})
				if !ok || len(name) == 0 {
					return nil, bad("invalid tag name")
				}
				size := binary.LittleEndian.Uint32(b[i+17:])
				if size > format.MaxTagSize || int64(len(b)-i-21) < int64(size) {
					return nil, bad("invalid tag size")
				}
				r.ops = append(r.ops, &format.Tag{Name: string(name), Data: b[i+21 : i+21+int(size)]})
				i += 21 + int(size)
			default:
				return nil, bad(fmt.Sprintf("unknown operation %d", op))
			}
		}
		recs = append(recs, r)
	}
	return recs, nil
}

// readTags implements ~R: it applies a tag dump to the archive, in place
// through the append writer, which locks the archive, checks it for damage
// and replaces it atomically. Unlike UC2, it checks the whole dump first
// and changes nothing on errors. The long name and size tags stay as they
// are: the 8.3 aliases derive from the long names, and the size tags from
// the sizes.
func (a *app) readTags(c *cmd, arch, dump string) error {
	d, err := os.ReadFile(dump)
	if err != nil {
		return fatalf(sevCmdLine, "cannot read dumpfile %s (%v)", dump, err)
	}
	recs, err := parseDump(d)
	if err != nil {
		return fatalf(sevCmdLine, "dumpfile %s is invalid (%v)", dump, err)
	}
	f, err := os.OpenFile(arch, os.O_RDWR, 0)
	if err != nil {
		return archiveErr(arch, err)
	}
	defer f.Close()
	w, err := uc2.NewAppendWriter(f, c.opts(nil)...)
	if err != nil {
		return appendErr(arch, err)
	}
	t := newCTree(tags.Source(w).(*uc2.Reader), c)
	// UC2 keeps the tags in reverse order and adds each at the front; work
	// holds that list reversed, so that it ends in the order to store.
	work, names := map[*uc2.File][]format.Tag{}, map[*uc2.File]string{}
	var order []*uc2.File
	for _, r := range recs {
		if len(r.ops) == 0 {
			continue // UC2 does not even look these up
		}
		e := t.entry(t.oem(r.path))
		if e == nil {
			w.Close() // unchanged
			return fatalf(sevCmdLine, "dumpfile %s names %s, which is not in %s", dump, t.oem(r.path), arch)
		}
		l, ok := work[e]
		if !ok {
			l, names[e] = slices.Clone(tags.Record(e).Tags), t.oem(r.path)
			order = append(order, e)
		}
		for _, op := range r.ops {
			if op == nil {
				l = nil
			} else {
				l = append(l, *op)
			}
		}
		work[e] = l
	}
	for _, e := range order {
		l := work[e]
		slices.Reverse(l)
		if !slices.Equal(managed(l), managed(tags.Record(e).Tags)) {
			a.warnf(sevSkipped, "long name and size tags of %s are managed and were not changed", names[e])
		}
		if err := tags.Replace(w, e, l); err != nil {
			return writeErr(arch, err) // w is not closed: nothing is written
		}
	}
	if err := w.Close(); err != nil {
		return writeErr(arch, err)
	}
	return nil
}

// managed returns the managed tags of l, as a sorted list.
func managed(l []format.Tag) []string {
	var s []string
	for _, x := range l {
		if x.Name == tagLongName || x.Name == tagUTF8Name || x.Name == tagSize64 {
			s = append(s, x.Name+"\x00"+string(x.Data))
		}
	}
	slices.Sort(s)
	return s
}

// killPath implements ~K: it empties a directory like UC2's SKillPath
// (DIRMAN.CPP), read-only and hidden entries included, but keeps what has
// ".U~K" in its name. The root confines it to dir; links are removed, not
// followed. Unlike UC2, which retried a directory until it gave up with
// FATAL ERROR 180, each failure is reported once.
func (a *app) killPath(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fatalf(sevChdir, "failed to change directory into %s", dir)
	}
	defer root.Close()
	a.kill(root, ".", nil)
	return nil
}

// kill deletes the contents of directory p, which must still be the one
// described by want, unless p is the top. It reports whether it could read
// the directory.
func (a *app) kill(root *os.Root, p string, want fs.FileInfo) bool {
	d, err := root.Open(p)
	var list []fs.DirEntry
	if err == nil {
		fi, serr := d.Stat()
		switch {
		case serr != nil:
			err = serr
		case want != nil && !os.SameFile(fi, want):
			err = errors.New("it was replaced")
		default:
			list, err = d.ReadDir(-1)
		}
		d.Close()
	}
	if err != nil {
		a.errorf(sevRmdir, "failed to delete directory %s (%v)", disp(p, 0), err)
		return false
	}
	slices.SortFunc(list, func(x, y fs.DirEntry) int { return strings.Compare(x.Name(), y.Name()) })
	for _, e := range list {
		if strings.Contains(strings.ToUpper(e.Name()), ".U~K") {
			continue
		}
		ep := path.Join(p, e.Name())
		fi, err := root.Lstat(ep)
		if err == nil && fi.IsDir() {
			if a.kill(root, ep, fi) {
				if err := root.Remove(ep); err != nil {
					a.errorf(sevRmdir, "failed to delete directory %s (%v)", disp(ep, 0), err)
				}
			}
			continue
		}
		if err == nil {
			err = root.Remove(ep)
		}
		if err != nil {
			a.errorf(sevDelete, "failed to delete %s (%v)", disp(ep, 0), err)
		}
	}
	return true
}

// dumpf prints ~D output with UC2's CRLF line ends: front-ends such as
// Total Commander parse it and need them.
func (a *app) dumpf(format string, args ...any) {
	a.outf(strings.ReplaceAll(format, "\n", "\r\n"), args...)
}
