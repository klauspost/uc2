package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"

	"github.com/klauspost/uc2"
)

const (
	revDefault = -1 // newest revision (all revisions for V)
	revAll     = -2
)

type cmd struct {
	op          byte // A D E L V T P U O R, or ~
	tilde       byte // the letter of a ~ command: D X R K V M ~
	name        string
	freshen     bool
	move        bool
	recurse     bool
	force       bool
	incremental bool
	protect     bool
	unprotect   bool
	newer       bool
	level       uc2.Level
	dest        string
	destSrc     bool // ##: destination + source path
	archives    []string
	specs       []string
	excludes    []string
	rev         int
	charset     uc2.Charset
	threads     int
	commentFile string
}

type globals struct {
	verbosity     int
	help, version bool
	color         string // --color: auto, always or never
}

// atom is a command line word. Literal atoms follow "--" and have no special
// meaning. Script words are never GNU flags: UC2 scripts had none, and Total
// Commander's lists hold names such as -F.
type atom struct {
	s      string
	lit    bool
	script bool
}

var commandAliases = map[string]byte{
	"add": 'A', "move": 'M', "freshen": 'F', "delete": 'D', "extract": 'E', "list": 'L',
	"verbose": 'V', "test": 'T', "protect": 'P', "unprotect": 'U', "optimize": 'O', "comment": 'R',
}

var commandNames = map[byte]string{
	'A': "add", 'D': "delete", 'E': "extract", 'L': "list", 'V': "verbose list", 'T': "test&repair",
	'P': "damage protect", 'U': "damage unprotect", 'O': "optimize", 'R': "revise comment",
}

var shortFlags = map[string]string{
	"r": "recurse", "f": "force", "i": "incremental", "v": "verbose", "q": "quiet",
	"h": "help", "?": "help", "d": "dest", "x": "exclude",
}

var valueFlags = map[string]bool{
	"level": true, "dest": true, "exclude": true, "rev": true, "charset": true, "threads": true, "comment-file": true, "color": true,
}

// Extended options of the original that are not implemented.
var unsupportedExt = map[string]bool{
	"DTT": true, "CONTAINS": true, "QUERY": true, "VLAB": true, "RELIA": true, "BAN": true, "TSN": true,
	"ARCA": true, "NOF": true, "BAK": true, "RAB": true, "ASUB": true, "EFA": true, "ARCON": true,
	"SMSKIP": true, "VSCAN": true, "SOS2EA": true, "SYSHID": true, "NET": true, "NOLOCK": true,
	"ELD": true, "EED": true, "FILTER": true,
}

// isHelpWord reports whether s asks for help: --help, or like UC2
// (MAIN.CPP:1311) any word that starts with ?, h or H, possibly after a
// '-' or '/'. No command starts so.
func isHelpWord(s string) bool {
	if strings.EqualFold(s, "--help") {
		return true
	}
	if optionPrefix(s) {
		s = s[1:]
	}
	return s != "" && strings.IndexByte("?hH", s[0]) >= 0
}

func optionPrefix(s string) bool {
	return len(s) > 1 && (s[0] == '-' || s[0] == '/' && runtime.GOOS == "windows")
}

// commandWord returns the word of the first command: the first word after
// the leading flags and their values.
func commandWord(args []string) string {
	for i := 0; i < len(args); i++ {
		if !isFlag(args[i], false) {
			return args[i]
		}
		if name, _, hasVal := strings.Cut(args[i][2:], "="); valueFlags[strings.ToLower(name)] && !hasVal {
			i++
		}
	}
	return ""
}

func parseArgs(args []string) ([]*cmd, globals, error) {
	g := globals{verbosity: normal}
	atoms, err := expandScripts(args)
	if err != nil {
		return nil, g, err
	}
	var cmds []*cmd
	start := 0
	for i := 0; i <= len(atoms); i++ {
		if i < len(atoms) && (atoms[i].lit || atoms[i].s != "&") {
			continue
		}
		c, err := parseCmd(atoms[start:i], &g)
		if err != nil {
			return nil, g, err
		}
		if c != nil {
			cmds = append(cmds, c)
		}
		start = i + 1
	}
	return cmds, g, nil
}

// expandScripts replaces @name atoms by the words of the file name.USC or name
// and marks everything after "--" literal, as well as command line words
// that name existing files but would otherwise be special.
func expandScripts(args []string) ([]atom, error) {
	var out []atom
	lit, scripts := false, 0
	var walk func([]atom, bool) error
	walk = func(list []atom, top bool) error {
		for _, at := range list {
			s := at.s
			switch {
			case lit || at.lit || top && shellName(s):
				out = append(out, atom{s: s, lit: true})
			case s == "--":
				lit = true
			case len(s) > 1 && s[0] == '@':
				if scripts++; scripts > 250 {
					return fatalf(sevCmdLine, "more than 250 files specified with @ (cyclic reference?)")
				}
				words, err := readScript(s[1:])
				if err != nil {
					return err
				}
				if err := walk(words, false); err != nil {
					return err
				}
			default:
				out = append(out, atom{s: s, script: !top})
			}
		}
		return nil
	}
	top := make([]atom, len(args))
	for i, s := range args {
		top[i].s = s
	}
	return out, walk(top, true)
}

// shellName reports whether s is an existing file with a name that starts
// like an option, script, command separator, destination or exclusion.
// Such names come from shell wildcards: "uc2 a arch *" must not turn a
// planted file "@x" into a script or "--move" into an option.
func shellName(s string) bool {
	if s == "--" || s == "" || !strings.ContainsRune("@&-#!", rune(s[0])) {
		return false
	}
	_, err := os.Lstat(s)
	return err == nil
}

// readScript returns the words of a script. A line naming an existing file
// is one literal word: Total Commander lists names one per line, unquoted,
// and names such as "#1 draft.txt" or "@x" must stay names.
func readScript(name string) ([]atom, error) {
	for _, n := range []string{name + ".USC", name} {
		b, err := os.ReadFile(n)
		if err != nil {
			continue
		}
		var words []atom
		for line := range strings.Lines(string(b)) {
			line = strings.TrimFunc(line, scriptSpace)
			if _, err := os.Lstat(line); err == nil {
				words = append(words, atom{s: line, lit: true})
				continue
			}
			for _, w := range strings.FieldsFunc(line, scriptSpace) {
				words = append(words, atom{s: w})
			}
		}
		return words, nil
	}
	return nil, fatalf(sevCmdLine, "cannot find script file %s", name)
}

func scriptSpace(r rune) bool { return unicode.IsSpace(r) || r == 0x1A }

// isFlag reports whether s is a GNU style flag. Short flags are only
// recognized after the command, where "-x" would otherwise be the command X.
func isFlag(s string, afterCmd bool) bool {
	if strings.HasPrefix(s, "--") && len(s) > 2 {
		return true
	}
	if !afterCmd || len(s) != 2 || s[0] != '-' {
		return false
	}
	_, ok := shortFlags[strings.ToLower(s[1:])]
	return ok
}

func parseCmd(seg []atom, g *globals) (*cmd, error) {
	c := &cmd{rev: revDefault}
	var pos []atom
	for i := 0; i < len(seg); i++ {
		at := seg[i]
		if at.lit || at.script || !isFlag(at.s, len(pos) > 0) {
			pos = append(pos, at)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(at.s, "-"), "=")
		name = strings.ToLower(name)
		if !strings.HasPrefix(at.s, "--") {
			name = shortFlags[name]
		}
		switch {
		case valueFlags[name] && !hasVal:
			if i++; i >= len(seg) {
				return nil, fatalf(sevCmdLine, "option %s needs a value", at.s)
			}
			val = seg[i].s
		case !valueFlags[name] && hasVal:
			return nil, fatalf(sevCmdLine, "option %s does not take a value", at.s)
		}
		if err := c.setFlag(name, val, g); err != nil {
			return nil, err
		}
	}
	if len(pos) == 0 {
		return nil, nil
	}
	if err := c.parseCommand(pos[0].s); err != nil {
		return nil, err
	}
	if c.op == '~' && c.tilde != 'D' {
		return c, c.tildeArgs(pos[1:])
	}
	i := 1
options:
	for ; i < len(pos) && !pos[i].lit; i++ {
		s := pos[i].s
		switch {
		case strings.HasPrefix(s, "#"):
			c.setDest(s[1:])
		case strings.HasPrefix(s, "!"):
			if err := c.extOption(s[1:]); err != nil {
				return nil, err
			}
		case optionPrefix(s):
			if err := c.parseLetters(s[1:]); err != nil {
				return nil, err
			}
		default:
			break options
		}
	}
	if i >= len(pos) || pos[i].s == "" {
		return nil, fatalf(sevCmdLine, "no archive specified")
	}
	c.archives = []string{pos[i].s}
	for _, at := range pos[i+1:] {
		s := at.s
		switch {
		case s == "":
		case strings.ContainsRune("TPUOR", rune(c.op)):
			c.archives = append(c.archives, s)
		case at.lit:
			c.specs = append(c.specs, s)
		case s[0] == '#':
			c.setDest(s[1:])
		case s[0] == '!':
			if len(s) > 1 {
				c.excludes = append(c.excludes, s[1:])
			}
		default:
			c.specs = append(c.specs, s)
		}
	}
	if c.op == '~' {
		return c, nil // ~D accepts options without using them
	}
	return c, c.validate()
}

// tildeArgs takes the words of ~X, ~R, ~K and ~V verbatim, like UC2, but
// rejects more words, which UC2 would run as the next command.
func (c *cmd) tildeArgs(words []atom) error {
	need := map[byte][]string{'X': {"archive", "dumpfile"}, 'R': {"archive", "dumpfile"}, 'K': {"path"}, 'V': {"file"}}[c.tilde]
	for i, what := range need {
		if i >= len(words) || words[i].s == "" {
			return fatalf(sevCmdLine, "no %s specified", what)
		}
		c.specs = append(c.specs, words[i].s)
	}
	if len(words) > len(need) {
		return fatalf(sevCmdLine, "unexpected parameter %s", words[len(need)].s)
	}
	return nil
}

func (c *cmd) setFlag(name, val string, g *globals) error {
	switch name {
	case "recurse":
		c.recurse = true
	case "move":
		c.move = true
	case "force", "yes":
		c.force = true
	case "incremental":
		c.incremental = true
	case "basic":
		c.incremental = false
	case "protect":
		c.protect = true
	case "unprotect":
		c.unprotect = true
	case "newer":
		c.newer = true
	case "fast", "normal", "tight", "super":
		c.level = levels[name]
	case "level":
		l, ok := levels[strings.ToLower(val)]
		if !ok {
			return fatalf(sevCmdLine, "unknown level %q (use fast, normal, tight or super)", val)
		}
		c.level = l
	case "dest":
		c.dest = val
	case "exclude":
		c.excludes = append(c.excludes, val)
	case "rev":
		n, err := strconv.Atoi(val)
		switch {
		case val == "*" || strings.EqualFold(val, "all"):
			c.rev = revAll
		case err == nil && n >= 0:
			c.rev = n
		default:
			return fatalf(sevCmdLine, "invalid revision %q", val)
		}
	case "charset":
		switch strings.TrimPrefix(strings.ToLower(val), "cp") {
		case "437":
			c.charset = uc2.CP437
		case "850":
			c.charset = uc2.CP850
		default:
			return fatalf(sevCmdLine, "unsupported charset %q (use 437 or 850)", val)
		}
	case "threads":
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fatalf(sevCmdLine, "invalid thread count %q", val)
		}
		c.threads = n
	case "comment-file":
		c.commentFile = val
	case "color":
		if val != "auto" && val != "always" && val != "never" {
			return fatalf(sevCmdLine, "invalid color mode %q (use auto, always or never)", val)
		}
		g.color = val
	case "verbose":
		g.verbosity = verbose
	case "quiet":
		g.verbosity = quiet
	case "help":
		g.help = true
	case "version":
		g.version = true
	default:
		return fatalf(sevCmdLine, "unknown option --%s", name)
	}
	return nil
}

var levels = map[string]uc2.Level{"fast": uc2.Fast, "normal": uc2.Normal, "tight": uc2.Tight, "super": uc2.SuperTight, "supertight": uc2.SuperTight}

func (c *cmd) parseCommand(word string) error {
	w := word
	if optionPrefix(w) {
		w = w[1:]
	}
	if op, ok := commandAliases[strings.ToLower(w)]; ok {
		w = string(op)
	}
	if w == "" {
		return fatalf(sevCmdLine, "no command specified")
	}
	op := upper(w[0])
	switch op {
	case 'A', 'M', 'F', 'D', 'E', 'X', 'L', 'V', 'T', 'P', 'U', 'O', 'R':
	case '~':
		if len(w) > 1 {
			c.tilde = upper(w[1])
		}
		c.op = op
		switch c.tilde {
		case 'D':
			c.recurse = true
			return c.parseLetters(w[2:])
		case 'X', 'R', 'K', 'V', 'M', '~': // the rest of ~M and ~~ is ignored
			return nil
		}
		return fatalf(sevCmdLine, "unknown command %s", word)
	case 'C', '$':
		return fatalf(sevCmdLine, "command %s is not supported", word)
	default:
		return fatalf(sevCmdLine, "unknown command %s", word)
	}
	switch op {
	case 'M':
		op, c.move = 'A', true
	case 'F':
		op, c.freshen = 'A', true
	case 'X':
		op = 'E'
	}
	c.op, c.name = op, commandNames[op]
	return c.parseLetters(w[1:])
}

func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 'a' + 'A'
	}
	return b
}

// parseLetters parses concatenated option letters such as "STF".
func (c *cmd) parseLetters(s string) error {
	for i := 0; i < len(s); i++ {
		switch upper(s[i]) {
		case 'M':
			c.move = true
		case 'S':
			c.recurse = true
		case 'F':
			c.force = true
		case 'I':
			c.incremental = true
		case 'B':
			c.incremental = false
		case 'P':
			c.protect = true
		case 'U':
			c.unprotect = true
		case '#':
			c.setDest(s[i+1:])
			return nil
		case '!':
			return c.extOption(s[i+1:])
		case 'T':
			rest := strings.ToUpper(s[i+1:])
			switch {
			case strings.HasPrefix(rest, "F"):
				c.level = uc2.Fast
			case strings.HasPrefix(rest, "N"):
				c.level = uc2.Normal
			case strings.HasPrefix(rest, "T"):
				c.level = uc2.Tight
			case strings.HasPrefix(rest, "ST"):
				c.level = uc2.SuperTight
				i++
			case rest == "":
				return fatalf(sevCmdLine, "unknown option -T")
			default:
				return fatalf(sevCmdLine, "unknown option -T%c", rest[0])
			}
			i++
		default:
			return fatalf(sevCmdLine, "unknown option -%c", s[i])
		}
	}
	return nil
}

func (c *cmd) setDest(s string) {
	c.destSrc = strings.HasPrefix(s, "#")
	c.dest = strings.TrimPrefix(s, "#")
}

func (c *cmd) extOption(s string) error {
	name, _, _ := strings.Cut(strings.ToUpper(s), "=")
	switch {
	case name == "NEWER":
		c.newer = true
		return nil
	case unsupportedExt[name]:
		return fatalf(sevCmdLine, "option !%s is not supported", name)
	}
	return fatalf(sevCmdLine, "unknown option !%s", s)
}

func (c *cmd) validate() error {
	switch {
	case c.move && c.op != 'A' && c.op != 'E':
		return fatalf(sevCmdLine, "Move mode is not possible for '%s'", c.name)
	case c.protect && c.unprotect:
		return fatalf(sevCmdLine, "options P and U cannot be combined")
	case c.op == 'D' && len(c.specs) == 0:
		return fatalf(sevCmdLine, "to delete all files *.* is needed")
	}
	return nil
}

// opts returns the library options; protect is nil to keep the current state.
func (c *cmd) opts(protect *bool) []uc2.Option {
	var o []uc2.Option
	if c.charset != nil {
		o = append(o, uc2.WithCharset(c.charset))
	}
	if c.threads > 0 {
		o = append(o, uc2.WithConcurrency(c.threads))
	}
	if c.level != 0 {
		o = append(o, uc2.WithLevel(c.level))
	}
	if protect != nil {
		o = append(o, uc2.WithDamageProtection(*protect))
	}
	return o
}

// protection returns the damage protection state for an archive that has cur.
func (c *cmd) protection(cur bool) *bool {
	on := (cur || c.protect) && !c.unprotect
	return &on
}

// archives resolves the archive names: default extension .UC2 and wildcards.
func (a *app) archives(c *cmd) []string {
	var out []string
	for _, spec := range c.archives {
		name := spec
		base := filepath.Base(name)
		switch {
		case strings.HasSuffix(name, ".") && base != "." && base != "..":
			name = strings.TrimSuffix(name, ".")
		case !strings.Contains(base, "."):
			name += ".UC2"
		}
		// Only '*' and '?' are wildcards, as in UC2.
		if !strings.ContainsAny(filepath.Base(name), "*?") {
			out = append(out, name)
			continue
		}
		pat := name
		if runtime.GOOS != "windows" {
			pat = strings.ReplaceAll(pat, `\`, `\\`)
		}
		m, _ := filepath.Glob(strings.ReplaceAll(pat, "[", "[[]"))
		n := len(out)
		for _, p := range m {
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				out = append(out, p)
			}
		}
		if len(out) == n {
			a.warnf(sevNoMatch, "no archive found matching %s", name)
		}
	}
	return out
}
