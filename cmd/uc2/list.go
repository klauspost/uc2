package main

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/klauspost/uc2"
)

type lister struct {
	a       *app
	c       *cmd
	t       *tree
	excl    []*mask
	verbose bool
	defRev  int
	listed  map[*uc2.File]bool
	visited map[string]bool // directories listed for the current mask group
	count   int64
	total   int64
}

// list implements L (short list) and V (verbose list) in the formats of
// UC2's LIST.CPP.
func (a *app) list(c *cmd, arch string) error {
	a.header("Listing files from", c, arch)
	r, err := a.openArchive(c, arch)
	if err != nil {
		return err
	}
	defer r.Close()
	fi, err := os.Stat(arch)
	if err != nil {
		return archiveErr(arch, err)
	}
	l := &lister{a: a, c: c, t: newTree(&r.Reader), excl: compileExcludes(c.excludes), verbose: c.op == 'V', listed: map[*uc2.File]bool{}}
	if l.verbose {
		l.defRev = revAll
	}
	// Masks are grouped by directory; each group lists its directory.
	var groups [][]*mask
	index := map[string]int{}
	for _, m := range compileMasks(c.specs, "*.*") {
		k := strings.ToLower(strings.Join(m.dirs, "/"))
		if i, ok := index[k]; ok {
			groups[i] = append(groups[i], m)
			continue
		}
		index[k] = len(groups)
		groups = append(groups, []*mask{m})
	}
	for _, g := range groups {
		if d, ok := l.t.findDir(g[0].dirs); ok {
			l.visited = map[string]bool{}
			l.dir(d, g, true, nil)
		}
	}
	l.trailer(&r.Reader, fi.Size())
	return nil
}

// dir lists directory d. via is a mask that matched a parent directory
// (with S), selecting everything below it.
func (l *lister) dir(d string, masks []*mask, top bool, via *mask) {
	// Hostile archives can hold sibling directories with the same name;
	// visiting each of them would be exponential in the depth.
	if l.visited[d] {
		return
	}
	l.visited[d] = true
	var sel []*uc2.File
	for _, f := range l.t.files[d] {
		if !l.listed[f] && l.match(f, masks, via) {
			l.listed[f] = true
			sel = append(sel, f)
			l.count++
			l.total += f.Size
		}
	}
	subs := l.t.children[d]
	a := l.a
	header := `\` + strings.ReplaceAll(d, "/", `\`)
	switch {
	case l.verbose:
		if !top {
			a.outf(cH+"%s\n", strings.Repeat("-", 76))
		}
		a.outf(cH+"--> Directory of %s\n"+cN+"\n", header)
		for _, s := range subs {
			base, ext, _ := strings.Cut(s.ShortName, ".")
			a.outf("%-8s %-3s <DIR>%s\n", base, ext, longName(s))
		}
		for _, f := range sel {
			l.line(f)
		}
		l.summary(sel)
	case top || len(sel) > 0:
		a.outf(cH+"--> Directory of %s\n"+cN, header)
		n := 0
		item := func(s string) {
			if n++; n%5 == 0 {
				a.outf("%s\n", s)
			} else {
				a.outf("%s ", s)
			}
		}
		for _, s := range subs {
			item(fmt.Sprintf("[%-14s", s.ShortName+"]"))
		}
		for _, f := range sel {
			name := f.ShortName
			if f.Revision > 0 {
				name += fmt.Sprintf(";%d", f.Revision)
			}
			item(fmt.Sprintf("%-15s", name))
		}
		if n%5 != 0 {
			a.outf("\n")
		}
	}
	if !l.c.recurse {
		return
	}
	for _, s := range subs {
		v := via
		for _, m := range masks {
			if v == nil && m.tree && m.matchName(path.Base(strings.TrimSuffix(s.Name, "/")), s.ShortName) {
				v = m
			}
		}
		l.dir(s.Name, masks, false, v)
	}
}

func (l *lister) match(f *uc2.File, masks []*mask, via *mask) bool {
	long, short := l.t.elems(f)
	if excluded(l.excl, long, short) {
		return false
	}
	for _, m := range masks {
		if (m == via || m.matchName(long[len(long)-1], f.ShortName)) && m.revOK(f.Revision, l.c.rev, l.defRev) && !(m.wild && isInternal(f)) {
			return true
		}
	}
	return false
}

func (l *lister) line(f *uc2.File) {
	base, ext, _ := strings.Cut(f.ShortName, ".")
	s := fmt.Sprintf("%-8s %-3s    %9d  ", base, ext, f.Size)
	if f.Revision > 0 {
		s = fmt.Sprintf("%-8s %-3s;%-2d %9d  ", base, ext, f.Revision, f.Size)
	}
	s += listDate(f.Modified) + " "
	for _, at := range []struct {
		a    uc2.Attr
		name string
	}{{uc2.AttrArchive, " Arch"}, {uc2.AttrReadOnly, " R/O"}, {uc2.AttrSystem, " Sys"}, {uc2.AttrHidden, " Hid"}} {
		if f.Attr&at.a != 0 {
			s += at.name
		}
	}
	l.a.outf("%s%s\n", s, longName(f))
}

// longName returns the long name of f, prefixed by a space, if it differs from the 8.3 name.
func longName(f *uc2.File) string {
	if n := path.Base(strings.TrimSuffix(f.Name, "/")); n != f.ShortName {
		return " " + n
	}
	return ""
}

func (l *lister) summary(sel []*uc2.File) {
	names := map[string]bool{}
	for _, f := range sel {
		names[f.Name] = true
	}
	files, revs := len(names), len(sel)
	a := l.a
	switch {
	case revs > files && files == 1:
		a.outf("      1 matching file (%d file revisions)\n", revs)
	case revs > files:
		a.outf("%7d matching files (%d file revisions)\n", files, revs)
	case files > 1:
		a.outf("%7d matching files\n", files)
	case files == 0:
		a.outf("     no matching files\n")
	default:
		a.outf("     1 matching file\n")
	}
}

func (l *lister) trailer(r *uc2.Reader, size int64) {
	var files, dirs, total int64
	for _, f := range r.File {
		if isDir(f) {
			dirs++
		} else {
			files++
			total += f.Size
		}
	}
	a := l.a
	rat, hasRatio := ratio(size, total)
	if !l.verbose {
		a.outf(cH+"files listed = %s (%s bytes)\n", neat(l.count), neat(l.total))
		if hasRatio {
			a.outf(cOK+"compression ratio = %s\n", rat)
		}
		return
	}
	prot, label := "NOT ", "archive has no volume label"
	if r.Protected {
		prot = ""
	}
	if r.Label != "" {
		label = `archive volume label is "` + r.Label + `"`
	}
	rev := r.MadeBy % 100
	if rev == 0 {
		rev = 1
	}
	// The library does not expose the serial number, which UC2 shows
	// after the two spaces; archives of unregistered copies have none.
	a.outf("\n"+cH+"Archive is %sdamage protected, %s\n", prot, label)
	a.outf(cOK+"Archive created by UltraCompressor II revision %d  \n", rev)
	a.outf(cH+"files listed           = %-8stotal length listed files = %s bytes\n", neat(l.count), neat(l.total))
	a.outf(cH+"files in archive       = %-8stotal length all files    = %s bytes\n", neat(files), neat(total))
	a.outf(cH+"directories in archive = %-8sarchive length            = %s bytes\n", neat(dirs), neat(size))
	if hasRatio {
		a.outf(cOK+"compression ratio      = %s\n", rat)
	}
}

func listDate(t time.Time) string {
	return fmt.Sprintf("%s-%02d-%4d  %2d:%02d:%02d", strings.ToUpper(t.Month().String()[:3]),
		t.Day(), t.Year(), t.Hour(), t.Minute(), t.Second())
}

// ratio formats the compression ratio like UC2, which scales both sizes
// down to at most 1,000,000 first. It is only shown above 1:1.
func ratio(archive, total int64) (string, bool) {
	for archive > 1000000 || total > 1000000 {
		archive /= 10
		total /= 10
	}
	if archive == 0 || total == 0 {
		return "", false
	}
	rat, left := total*100/archive, archive*1000/total
	if rat <= 100 {
		return "", false
	}
	return fmt.Sprintf("1:%d.%02d  (%d.%d%% reduction, %d.%d%% left)", rat/100, rat%100, (1000-left)/10, (1000-left)%10, left/10, left%10), true
}
