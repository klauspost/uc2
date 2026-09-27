package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRewriteKeepsACL(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	uc2Run(t, 0, "a", "arch", "a.txt", "b.txt")
	if out, err := exec.Command("icacls", "arch.UC2", "/inheritance:r", "/grant:r", os.Getenv("USERNAME")+":F").CombinedOutput(); err != nil {
		t.Skipf("icacls: %v\n%s", err, out)
	}
	p, _ := syscall.UTF16PtrFromString("arch.UC2")
	if err := syscall.SetFileAttributes(p, syscall.FILE_ATTRIBUTE_HIDDEN); err != nil {
		t.Fatal(err)
	}
	uc2Run(t, 0, "d", "arch", "b.txt")
	checkContents(t, "arch", map[string]string{"a.txt": "a"})
	if out, _ := exec.Command("icacls", "arch.UC2").CombinedOutput(); strings.Contains(string(out), "(I)") {
		t.Errorf("rewritten archive has inherited permissions:\n%s", out)
	}
	if attr, err := syscall.GetFileAttributes(p); err != nil || attr&syscall.FILE_ATTRIBUTE_HIDDEN == 0 {
		t.Errorf("attributes %#x (%v), not hidden", attr, err)
	}
}

// readOnly reads the attribute through a handle: the directory entry of a
// hard link can be stale.
func readOnly(t *testing.T, name string) bool {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode()&0o200 == 0
}

func TestExtractReadOnlyLinks(t *testing.T) {
	t.Chdir(t.TempDir())
	writeArchive(t, "arch.UC2", "a.txt", "b.txt")
	writeFiles(t, map[string]string{"other/orig.txt": "orig", "dest/b.txt": "locked"}, stamp.Add(time.Hour))
	os.Chmod("other/orig.txt", 0o444)
	os.Chmod("dest/b.txt", 0o444)
	if err := os.Link("other/orig.txt", "dest/a.txt"); err != nil {
		t.Skip("no hard links:", err)
	}
	// Go opens files without FILE_SHARE_DELETE: b.txt cannot be replaced.
	lock, err := os.Open("dest/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	contains(t, uc2Run(t, sevWrite, "ef", "#dest", "arch"), " ERROR 80: cannot write to b.txt, file skipped")
	lock.Close()
	checkFile(t, "dest/a.txt", "archived 0")
	checkFile(t, "other/orig.txt", "orig")
	checkFile(t, "dest/b.txt", "locked")
	for _, name := range []string{"other/orig.txt", "dest/b.txt"} {
		if !readOnly(t, name) {
			t.Errorf("%s lost its read-only attribute", name)
		}
	}
}
