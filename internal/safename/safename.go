// Package safename recognizes path elements that are dangerous to create.
package safename

import "strings"

// DotGit reports whether elem names a .git directory, directly or through a
// file system alias: any case, trailing dots or spaces and a stream suffix
// (Windows), the NTFS short name GIT~N, and code points HFS+ ignores.
func DotGit(elem string) bool {
	e := strings.Map(func(r rune) rune {
		if r >= 0x200C && r <= 0x200F || r >= 0x202A && r <= 0x202E || r >= 0x206A && r <= 0x206F || r == 0xFEFF {
			return -1
		}
		return r
	}, elem)
	e, _, _ = strings.Cut(e, ":")
	e = strings.ToLower(strings.TrimRight(e, ". "))
	n, alias := strings.CutPrefix(e, "git~")
	return e == ".git" || alias && n != "" && strings.Trim(n, "0123456789") == ""
}

// Alias reports whether elem refers to a .git directory without being
// spelled ".git"; such names are never legitimate.
func Alias(elem string) bool { return elem != ".git" && DotGit(elem) }
