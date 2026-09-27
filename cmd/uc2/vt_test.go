package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// vt emulates the part of a terminal that the full-screen help uses: text,
// CR, LF (which returns the carriage too unless in raw mode), BS,
// scrolling, cursor addressing, erasing, SGR attributes per cell, and the
// cursor and autowrap modes. Anything else is recorded in bad.
type vt struct {
	w, h, x, y int // x == w: a wrap is pending
	cells      [][]cell
	sgr        string
	modes      map[string]bool
	raw        bool
	bad        []string
}

type cell struct {
	r   rune
	sgr string
}

func newVT(w, h int) *vt {
	v := &vt{w: w, h: h, modes: map[string]bool{"?7": true, "?25": true}}
	v.cells = make([][]cell, h)
	for y := range v.cells {
		v.cells[y] = v.blank()
	}
	return v
}

func (v *vt) blank() []cell {
	r := make([]cell, v.w)
	for i := range r {
		r[i].r = ' '
	}
	return r
}

// resize changes the size, keeping the top left of the screen.
func (v *vt) resize(w, h int) {
	old := v.cells
	v.w, v.h = w, h
	v.cells = make([][]cell, h)
	for y := range v.cells {
		v.cells[y] = v.blank()
		if y < len(old) {
			copy(v.cells[y], old[y])
		}
	}
	v.x, v.y = min(v.x, w-1), min(v.y, h-1)
}

var csiRE = regexp.MustCompile(`^\x1b\[([?0-9;]*)([@-~])`)

func (v *vt) Write(p []byte) (int, error) {
	for s := string(p); s != ""; {
		if s[0] == 0x1b {
			m := csiRE.FindStringSubmatch(s)
			if m == nil {
				v.bad = append(v.bad, fmt.Sprintf("%q", s[:min(len(s), 6)]))
				s = s[1:]
				continue
			}
			v.csi(m[1], m[2])
			s = s[len(m[0]):]
			continue
		}
		r, n := utf8.DecodeRuneInString(s)
		s = s[n:]
		switch {
		case r == '\r':
			v.x = 0
		case r == '\n':
			v.x = min(v.x, v.w-1)
			if !v.raw {
				v.x = 0
			}
			v.lf()
		case r == '\b':
			v.x = max(min(v.x, v.w-1)-1, 0)
		case r < ' ' || r == 0x7f:
			v.bad = append(v.bad, fmt.Sprintf("%q", r))
		default:
			if v.x == v.w {
				v.x = 0
				v.lf()
			}
			v.cells[v.y][v.x] = cell{r, v.sgr}
			if v.x < v.w-1 || v.modes["?7"] {
				v.x++
			}
		}
	}
	return len(p), nil
}

func (v *vt) lf() {
	if v.y < v.h-1 {
		v.y++
		return
	}
	v.cells = append(v.cells[1:], v.blank())
}

func (v *vt) csi(par, fin string) {
	nums := strings.Split(par, ";")
	n := func(i int) int {
		if i < len(nums) {
			if k, err := strconv.Atoi(nums[i]); err == nil && k > 0 {
				return k
			}
		}
		return 1
	}
	switch {
	case fin == "H":
		v.y, v.x = min(n(0), v.h)-1, min(n(1), v.w)-1
	case fin == "J" && par == "2":
		for y := range v.cells {
			v.cells[y] = v.blank()
		}
	case fin == "K" && par == "":
		for x := min(v.x, v.w-1); x < v.w; x++ {
			v.cells[v.y][x] = cell{' ', v.sgr}
		}
	case fin == "m":
		for len(nums) > 0 && (nums[0] == "0" || nums[0] == "") {
			nums = nums[1:]
		}
		v.sgr = strings.Join(nums, ";")
	case (fin == "h" || fin == "l") && (par == "?7" || par == "?25"):
		v.modes[par] = fin == "h"
	default:
		v.bad = append(v.bad, "CSI "+par+fin)
	}
}

// lines returns the text of the screen, without trailing blanks.
func (v *vt) lines() []string {
	out := make([]string, v.h)
	for y := range v.cells {
		out[y] = v.line(y)
	}
	return out
}

func (v *vt) line(y int) string {
	var sb strings.Builder
	for _, c := range v.cells[y] {
		sb.WriteRune(c.r)
	}
	return strings.TrimRight(sb.String(), " ")
}

// markup returns row y with {sgr} at each change of the attributes, like
// the capture files of UC2, up to the last cell that is not a blank in
// the default attributes.
func (v *vt) markup(y int) string {
	row := v.cells[y]
	end := len(row)
	for end > 0 && row[end-1] == (cell{' ', ""}) {
		end--
	}
	var sb strings.Builder
	cur := ""
	for _, c := range row[:end] {
		if c.sgr != cur {
			cur = c.sgr
			sb.WriteString("{" + cur + "}")
		}
		sb.WriteRune(c.r)
	}
	return sb.String()
}

// attr returns the attributes of the cells in row y from x to x+n-1, or
// "mixed".
func (v *vt) attr(y, x, n int) string {
	a := v.cells[y][x].sgr
	for _, c := range v.cells[y][x : x+n] {
		if c.sgr != a {
			return "mixed"
		}
	}
	return a
}

func TestVT(t *testing.T) {
	v := newVT(10, 3)
	v.Write([]byte("hello\r\nworld\x1b[0;44m"))
	v.Write([]byte(wrapOff + "\x1b[2;3Habcdefghijklmnop\x1b[0m\x1b[3;1Hlast" + curOff))
	if l := v.lines(); l[0] != "hello" || l[1] != "woabcdefgp" || l[2] != "last" || v.cells[1][2].sgr != "44" || v.modes["?25"] || v.modes["?7"] {
		t.Errorf("no autowrap: %q %v", l, v.modes)
	}
	// Erasing at the right edge erases the last column.
	v.Write([]byte(wrapOn + "\x1b[1;1H0123456789\x1b[K"))
	if l := v.line(0); l != "012345678" {
		t.Errorf("erased %q", l)
	}
	// LF on the last row scrolls; unknown controls are errors.
	v.Write([]byte("\x1b[3;3H\n!\x07\x1b[5n"))
	if l := v.lines(); l[0] != "woabcdefgp" || l[1] != "last" || l[2] != "!" || len(v.bad) != 2 {
		t.Errorf("scrolled: %q %q", l, v.bad)
	}
	v.raw = true
	v.Write([]byte("\x1b[2J\x1b[1;4Hx\ny"))
	if l := v.lines(); l[0] != "   x" || l[1] != "    y" || v.markup(1) != "    y" {
		t.Errorf("raw: %q", l)
	}
}
