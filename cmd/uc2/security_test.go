package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/uc2"
)

func TestExtractDotGit(t *testing.T) {
	t.Chdir(t.TempDir())
	writeArchive(t, "repo.UC2", ".git/hooks/pre-commit", ".git/config", "a.txt")
	// A project backup restored into an empty directory keeps its repository.
	uc2Run(t, 0, "es", "#restore", "repo")
	checkFile(t, "restore/.git/hooks/pre-commit", "archived 0")
	checkFile(t, "restore/.git/config", "archived 1")
	// Existing repositories are not written to, including the restored one.
	writeFiles(t, map[string]string{"work/.git/config": "mine"}, stamp)
	for _, dir := range []string{"work", "restore"} {
		contains(t, uc2Run(t, sevSkipped, "esf", "#"+dir, "repo"),
			" WARNING 30: skipping "+disp(".git/hooks/pre-commit", 0)+" (refusing to write into existing .git directory)")
	}
	notExist(t, "work/.git/hooks")
	checkFile(t, "work/.git/config", "mine")
	checkFile(t, "work/a.txt", "archived 2")

	// Other names of .git are never written.
	writeArchive(t, "alias.UC2", ".Git/hooks/post-checkout")
	contains(t, uc2Run(t, sevWrite, "es", "#new", "alias"),
		" ERROR 80: cannot write to "+disp(".Git/hooks/post-checkout", 0)+", file skipped (invalid name)")
	notExist(t, "new/.Git")
	a := newTestApp("")
	a.mapped = map[string]bool{}
	for _, rel := range []string{"GIT~1/hooks/x", "sub/.git./config", ".git::$INDEX_ALLOCATION/x", ".git /x", ".GIT/config", "git~12"} {
		if target, ok := a.diskPath(rel); ok {
			t.Errorf("%q accepted as %q", rel, target)
		}
	}
}

func TestShellGlobNames(t *testing.T) {
	names := []string{"!a.txt", "#out", "&", "--move", "-f", "@x", "a.txt", "x"}
	files := map[string]string{}
	for _, n := range names {
		files[n] = "file " + n
	}
	files["x"] = "& d victim *.*"
	setup(t, files)
	writeArchive(t, "victim.UC2", "keep.txt")
	// What a shell makes of "uc2 a arch *": all of them are file names.
	uc2Run(t, 0, append([]string{"a", "arch"}, names...)...)
	checkContents(t, "arch", files)
	checkFile(t, "a.txt", "file a.txt")
	checkContents(t, "victim", map[string]string{"keep.txt": "archived 0"})
	// Words from scripts keep their meaning.
	os.WriteFile("s.USC", []byte("l victim & l arch"), 0o666)
	contains(t, uc2Run(t, 0, "@s"), "Listing files from victim.UC2", "Listing files from arch.UC2")
}

func TestRewriteRefusesDamage(t *testing.T) {
	setup(t, map[string]string{"a.bin": randomData(20000, 4), "b.txt": "b", "c.txt": "c"})
	uc2Run(t, 0, "ap", "arch", "a.bin", "b.txt")
	orig, _ := os.ReadFile("arch.UC2")
	corrupt(t, "arch.UC2", 2000)
	bad, _ := os.ReadFile("arch.UC2")
	for _, args := range [][]string{
		{"d", "arch", "b.txt"}, {"a", "arch", "c.txt"}, {"m", "arch", "c.txt"}, {"em", "#x", "arch", "b.txt"},
		{"r", "arch", "--comment-file", "c.txt"}, {"o", "arch"},
	} {
		contains(t, uc2Run(t, sevBroken, args...), "FATAL ERROR 200: archive arch.UC2 is damaged", "repair it with 'uc2 T'")
		if b, _ := os.ReadFile("arch.UC2"); !bytes.Equal(b, bad) {
			t.Fatalf("%q modified the damaged archive", args)
		}
	}
	notExist(t, "x/b.txt")
	checkFile(t, "c.txt", "c")
	uc2Run(t, sevDamaged, "t", "arch")
	if fixed, _ := os.ReadFile("FIX_0001.UC2"); !bytes.Equal(fixed, orig) {
		t.Error("damage is no longer repairable")
	}
}

func TestMoveStoresAgain(t *testing.T) {
	setup(t, map[string]string{"a.txt": "version 1"})
	uc2Run(t, 0, "a", "arch", "a.txt")
	// Same size and time as the archived revision: no smart skip in move mode.
	writeFiles(t, map[string]string{"a.txt": "VERSION 2"}, stamp)
	uc2Run(t, 0, "m", "arch", "a.txt")
	notExist(t, "a.txt")
	checkContents(t, "arch", map[string]string{"a.txt": "VERSION 2"})
}

func TestMoveKeepsChangedFiles(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a", "b.txt": "b", "c.txt": "c"})
	root, err := os.OpenRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var items []*item
	for _, n := range []string{"a.txt", "b.txt", "c.txt"} {
		fi, err := root.Lstat(n)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, &item{disk: n, root: root, rel: n, read: fi})
	}
	// a.txt grows after it was read; b.txt is replaced by a file with the
	// same size and time (created first, so it cannot reuse the inode).
	f, _ := os.OpenFile("a.txt", os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString(" more")
	f.Close()
	writeFiles(t, map[string]string{"b.new": "B"}, stamp)
	if err := os.Rename("b.new", "b.txt"); err != nil {
		t.Fatal(err)
	}
	a := newTestApp("")
	a.moveFiles(items)
	contains(t, a.stderr.(*bytes.Buffer).String(), " ERROR 55: failed to delete a.txt (file changed after it was added)",
		" ERROR 55: failed to delete b.txt (file changed after it was added)")
	checkFile(t, "a.txt", "a more")
	checkFile(t, "b.txt", "B")
	notExist(t, "c.txt")
}

func TestAddReplacedFile(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a"})
	root, err := os.OpenRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fi, err := root.Lstat("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	writeFiles(t, map[string]string{"new.txt": "swapped"}, stamp)
	if err := os.Rename("new.txt", "a.txt"); err != nil {
		t.Fatal(err)
	}
	a := newTestApp("")
	ad := &adder{a: a, c: &cmd{op: 'A'}, items: []*item{{disk: "a.txt", root: root, rel: "a.txt", name: "a.txt", info: fi}}}
	if err := a.rewrite("arch.UC2", nil, nil, func(w *uc2.Writer) error { return ad.write(w, false) }, nil); err != nil {
		t.Fatal(err)
	}
	contains(t, a.stderr.(*bytes.Buffer).String(), " WARNING 30: skipped file a.txt (file was replaced)")
	checkContents(t, "arch", map[string]string{})
}

func TestAddLinks(t *testing.T) {
	setup(t, map[string]string{"secret.txt": "secret", "drop/a.txt": "a", "drop/in.txt": "inside"})
	if err := os.Symlink(filepath.Join("..", "secret.txt"), filepath.Join("drop", "s.txt")); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := os.Symlink("in.txt", filepath.Join("drop", "l.txt")); err != nil {
		t.Fatal(err)
	}
	// File links are followed only inside the tree.
	contains(t, uc2Run(t, sevSkipped, "a", "arch", "drop/*.*"), " WARNING 30: skipped file "+filepath.Join("drop", "s.txt"))
	checkContents(t, "arch", map[string]string{"a.txt": "a", "in.txt": "inside", "l.txt": "inside"})
}

func TestMoveExtractSmartSkipContent(t *testing.T) {
	t.Chdir(t.TempDir())
	writeArchive(t, "arch.UC2", "doc.txt", "same.txt")
	// Both have the size and time of the archived revisions; doc.txt has other data.
	writeFiles(t, map[string]string{"out/doc.txt": "my edit 0!", "out/same.txt": "archived 1"}, stamp)
	contains(t, uc2Run(t, sevSkipped, "em", "#out", "arch"), "Smart skipping same.txt", " WARNING 30: skipping doc.txt (already exists)")
	checkFile(t, "out/doc.txt", "my edit 0!")
	checkContents(t, "arch", map[string]string{"doc.txt": "archived 0"})
}

func TestConcurrentUpdate(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	changes := map[string]func(){
		"touched": func() { os.Chtimes("arch.UC2", time.Now(), time.Now()) },
		"created": func() { writeArchive(t, "arch.UC2", "other.txt") },
	}
	if runtime.GOOS != "windows" { // Windows cannot rename over the open archive
		changes["replaced"] = func() {
			writeArchive(t, "new.UC2", "other.txt")
			os.Rename("new.UC2", "arch.UC2")
		}
	}
	for name, change := range changes {
		os.Remove("arch.UC2")
		var r *archive
		if name != "created" {
			uc2Run(t, 0, "a", "arch", "a.txt")
			var err error
			if r, err = newTestApp("").openArchive(&cmd{}, "arch.UC2"); err != nil {
				t.Fatal(err)
			}
		}
		before, _ := os.ReadFile("arch.UC2")
		err := newTestApp("").rewrite("arch.UC2", r, nil, func(w *uc2.Writer) error {
			change()
			before, _ = os.ReadFile("arch.UC2")
			return nil
		}, nil)
		if code(err) != sevWrite || !strings.Contains(err.Error(), "by another process") {
			t.Errorf("%s: got %v", name, err)
		}
		if b, _ := os.ReadFile("arch.UC2"); !bytes.Equal(b, before) {
			t.Errorf("%s: archive replaced", name)
		}
		if m, _ := filepath.Glob("U$~*"); len(m) > 0 {
			t.Errorf("%s: temporary files %q left", name, m)
		}
	}

	// The identity is taken when opening, not when comparing: a copy with
	// the same size and time is another file.
	r, err := newTestApp("").openArchive(&cmd{}, "arch.UC2")
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	b, _ := os.ReadFile("arch.UC2")
	os.WriteFile("new.UC2", b, 0o666)
	os.Chtimes("new.UC2", r.info.ModTime(), r.info.ModTime())
	if err := os.Rename("new.UC2", "arch.UC2"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat("arch.UC2"); err != nil || unchanged(fi, r.info) {
		t.Errorf("replaced archive not detected (%v)", err)
	}

	// In-place updates are locked against each other.
	f, err := os.OpenFile("arch.UC2", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w, err := uc2.NewAppendWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"p", "arch"}, {"ai", "arch", "b.txt"}} {
		contains(t, uc2Run(t, sevWrite, args...), "FATAL ERROR 80: cannot update arch.UC2 (it is being updated by another process)")
	}
	w.Close()
}

func TestInterruptRemovesTemps(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a"})
	uc2Run(t, 0, "a", "arch", "a.txt")
	a := newTestApp("")
	r, err := a.openArchive(&cmd{}, "arch.UC2")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	sig, exited := make(chan os.Signal, 1), make(chan int, 1)
	go onSignal(sig, io.Discard, func(code int) { exited <- code })
	err = a.rewrite("arch.UC2", r, nil, func(w *uc2.Writer) error {
		if m, _ := filepath.Glob("U$~*.TMP"); len(m) != 1 {
			t.Errorf("temporary files %q", m)
		}
		sig <- os.Interrupt
		if code := <-exited; code != sevAbort {
			t.Errorf("exit code %d", code)
		}
		if m, _ := filepath.Glob("U$~*"); len(m) != 0 {
			t.Errorf("temporary files %q left after the signal", m)
		}
		return errors.New("interrupted")
	}, nil)
	if err == nil {
		t.Error("rewrite succeeded")
	}
	checkContents(t, "arch", map[string]string{"a.txt": "a"})
}

func TestHostileFormatChars(t *testing.T) {
	t.Chdir(t.TempDir())
	const bad = "\u202e\u200b\u2028\u2029\ufeff\u061c"
	writeArchive(t, "names.UC2", "inv\u202efdp.exe", "a\u200bb\u2028c\u2029d\ufeff\u061c.txt")
	for _, args := range [][]string{{"v", "names"}, {"e", "#out", "names"}, {"t", "names"}} {
		out := uc2Run(t, 0, args...)
		if strings.ContainsAny(out, bad) {
			t.Errorf("%q: format characters in output:\n%q", args, out)
		}
		if args[0] == "v" {
			contains(t, out, "inv?fdp.exe", "a?b?c?d??.txt")
		}
	}
}
