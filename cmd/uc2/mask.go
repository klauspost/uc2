package main

import (
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/uc2"
	"github.com/klauspost/uc2/internal/format"
)

// mask is a file specification: a directory resolved from the archive root,
// a name pattern and a revision selector.
type mask struct {
	text  string
	dirs  []string
	name  string
	glob  string // lower case path.Match pattern of name
	fcb   [11]rune
	fcbOK bool
	wild  bool
	tree  bool // a matching directory selects its subtree
	rev   int
	used  bool
}

func compileMasks(specs []string, def string) []*mask {
	if len(specs) == 0 && def != "" {
		specs = []string{def}
	}
	ms := make([]*mask, len(specs))
	for i, s := range specs {
		ms[i] = compileMask(s)
	}
	return ms
}

// compileExcludes compiles exclusions, which also exclude the subtrees of
// directories matching a wildcard.
func compileExcludes(specs []string) []*mask {
	ms := compileMasks(specs, "")
	for _, m := range ms {
		m.tree = true
	}
	return ms
}

func compileMask(s string) *mask {
	text, rev := s, revDefault
	if i := strings.LastIndexByte(s, ';'); i >= 0 {
		if v := s[i+1:]; v == "*" {
			rev, s = revAll, s[:i]
		} else if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			rev, s = n, s[:i]
		}
	}
	s = strings.TrimLeft(strings.ReplaceAll(s, `\`, "/"), "/")
	if s == "" || strings.HasSuffix(s, "/") {
		s += "*.*"
	}
	var dirs []string
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		dirs, s = strings.Split(s[:i], "/"), s[i+1:]
	}
	m := newMask(s)
	m.text, m.dirs, m.rev = text, dirs, rev
	return m
}

// newMask compiles a name pattern. Like in UC2, only '*' and '?' are
// wildcards. Wildcards other than *.* only match files, so "test*" with S
// does not select everything below a directory "tests".
func newMask(name string) *mask {
	m := &mask{name: name, rev: revDefault, wild: strings.ContainsAny(name, "*?")}
	m.tree = !m.wild || name == "*.*"
	m.glob = strings.ReplaceAll(strings.ToLower(name), "[", "[[]")
	m.fcb, m.fcbOK = fcbPattern(name)
	return m
}

// fcbPattern compiles a DOS wildcard: '*' fills the rest of the name or
// extension field with '?'.
func fcbPattern(p string) (f [11]rune, ok bool) {
	base, ext := p, ""
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		base, ext = p[:i], p[i+1:]
	}
	fill := func(dst []rune, s string) bool {
		for i := range dst {
			dst[i] = ' '
		}
		j := 0
		for _, r := range strings.ToUpper(s) {
			if r == '*' {
				for ; j < len(dst); j++ {
					dst[j] = '?'
				}
				return true
			}
			if j == len(dst) || r == '.' {
				return false
			}
			dst[j] = r
			j++
		}
		return true
	}
	return f, fill(f[:8], base) && fill(f[8:], ext)
}

// fcbName converts an 8.3 name to its padded FCB form.
func fcbName(n string) (f [11]rune, ok bool) {
	if strings.ContainsAny(n, "*?") {
		return f, false
	}
	return fcbPattern(n)
}

// matchName matches a single path element by its long and (optional) 8.3 name.
func (m *mask) matchName(long, short string) bool {
	if m.name == "*.*" || strings.EqualFold(m.name, long) || short != "" && strings.EqualFold(m.name, short) {
		return true
	}
	if m.fcbOK {
		n := short
		if n == "" {
			n = long
		}
		if f, ok := fcbName(n); ok && fcbMatch(m.fcb, f) {
			return true
		}
	}
	if !m.wild {
		return false
	}
	ok, _ := path.Match(m.glob, strings.ToLower(long))
	return ok
}

func fcbMatch(p, n [11]rune) bool {
	for i := range p {
		if p[i] != '?' && p[i] != n[i] {
			return false
		}
	}
	return true
}

// matchPath matches a path given as elements (short may be nil). With
// recurse, the path may be below the mask directory and, for tree masks, a
// matching directory element selects everything below it.
func (m *mask) matchPath(long, short []string, recurse bool) bool {
	k := len(m.dirs)
	if len(long) <= k || !recurse && len(long) != k+1 {
		return false
	}
	if !elemsEqual(m.dirs, long, short) {
		return false
	}
	if !m.tree {
		k = len(long) - 1
	}
	for j := len(long) - 1; j >= k; j-- {
		s := ""
		if short != nil {
			s = short[j]
		}
		if m.matchName(long[j], s) {
			return true
		}
	}
	return false
}

func (m *mask) revOK(rev, global, def int) bool {
	want := m.rev
	if want == revDefault {
		want = global
	}
	if want == revDefault {
		want = def
	}
	return want == revAll || want == rev
}

func excluded(excl []*mask, long, short []string) bool {
	for _, e := range excl {
		if e.matchPath(long, short, true) {
			return true
		}
	}
	return false
}

// excludedDir also excludes a directory named by an exclusion like "dir\".
func excludedDir(excl []*mask, long, short []string) bool {
	for _, e := range excl {
		if e.matchPath(long, short, true) || e.name == "*.*" && len(e.dirs) == len(long) && elemsEqual(e.dirs, long, short) {
			return true
		}
	}
	return false
}

func (a *app) warnUnmatched(ms []*mask) {
	for _, m := range ms {
		if !m.used {
			a.warnf(sevNoMatch, "no file found matching %s", m.text)
		}
	}
}

// tree indexes an archive by directory.
type tree struct {
	r        *uc2.Reader
	short    map[string]string // long directory path -> 8.3 path, both "/"-terminated
	children map[string][]*uc2.File
	files    map[string][]*uc2.File
	dirList  []*uc2.File
}

func newTree(r *uc2.Reader) *tree {
	t := &tree{r: r, short: map[string]string{"": ""}, children: map[string][]*uc2.File{}, files: map[string][]*uc2.File{}}
	for _, f := range r.File {
		d := dirOf(f.Name)
		if isDir(f) {
			t.short[f.Name] = t.short[d] + f.ShortName + "/"
			t.children[d] = append(t.children[d], f)
			t.dirList = append(t.dirList, f)
		} else {
			t.files[d] = append(t.files[d], f)
		}
	}
	return t
}

func isDir(f *uc2.File) bool { return strings.HasSuffix(f.Name, "/") }

func isInternal(f *uc2.File) bool { return strings.HasPrefix(f.ShortName, "U$~") }

// dirOf returns the "/"-terminated parent directory of an archive name.
func dirOf(name string) string {
	return name[:strings.LastIndexByte(strings.TrimSuffix(name, "/"), '/')+1]
}

// elems returns the long and 8.3 path elements of f; short is nil if unknown.
func (t *tree) elems(f *uc2.File) (long, short []string) {
	long = strings.Split(strings.TrimSuffix(f.Name, "/"), "/")
	short = strings.Split(t.short[dirOf(f.Name)]+f.ShortName, "/")
	if len(short) != len(long) {
		short = nil
	}
	return long, short
}

// findDir returns the archive directory matching the elements of a mask directory.
func (t *tree) findDir(dirs []string) (string, bool) {
	if len(dirs) == 0 {
		return "", true
	}
	for _, d := range t.dirList {
		if long, short := t.elems(d); len(long) == len(dirs) && elemsEqual(dirs, long, short) {
			return d.Name, true
		}
	}
	return "", false
}

// elemsEqual reports whether want is a prefix of the path, matching each
// element by its long or 8.3 name.
func elemsEqual(want, long, short []string) bool {
	for i, w := range want {
		if !strings.EqualFold(w, long[i]) && (short == nil || !strings.EqualFold(w, short[i])) {
			return false
		}
	}
	return true
}

type selected struct {
	f   *uc2.File
	rel string // path below the mask directory; directories end in "/"
}

// selectFiles returns the revisions selected by masks and not excluded.
// Directories are only selected when recursing and withDirs is set.
func (t *tree) selectFiles(c *cmd, masks, excl []*mask, defRev int, withDirs bool) []selected {
	var out []selected
	for _, f := range t.r.File {
		dir := isDir(f)
		if dir && !(withDirs && c.recurse) {
			continue
		}
		long, short := t.elems(f)
		if dir && excludedDir(excl, long, short) || !dir && excluded(excl, long, short) {
			continue
		}
		for _, m := range masks {
			if dir && !m.tree || !m.matchPath(long, short, c.recurse) || !dir && (!m.revOK(f.Revision, c.rev, defRev) || m.wild && isInternal(f)) {
				continue
			}
			if !dir {
				m.used = true
			}
			rel := strings.Join(long[len(m.dirs):], "/")
			if c.destSrc {
				rel = strings.Join(long, "/")
			}
			if dir {
				rel += "/"
			}
			out = append(out, selected{f, rel})
			break
		}
	}
	return out
}

// disp formats an archive name for messages, DOS style with ";n" for older revisions.
func disp(name string, rev int) string {
	s := strings.ReplaceAll(strings.TrimSuffix(name, "/"), "/", `\`)
	if rev > 0 {
		s += ";" + strconv.Itoa(rev)
	}
	return s
}

func dispFile(f *uc2.File) string { return disp(f.Name, f.Revision) }

// dosStamp returns the DOS date and time of t, the resolution stored in archives.
func dosStamp(t time.Time) uint32 {
	d, tm := format.DOSTime(t.In(time.Local))
	return uint32(d)<<16 | uint32(tm)
}

// sameFile reports whether an archived revision and a disk file have the same
// size and DOS time, the criterion for smart skipping.
func sameFile(f *uc2.File, size int64, mod time.Time) bool {
	return f.Size == size && dosStamp(f.Modified) == dosStamp(mod)
}

// neat formats n with thousands separators.
func neat(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
