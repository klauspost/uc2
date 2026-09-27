//go:build unix

package main

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAddSwappedSpecialFile(t *testing.T) {
	setup(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	root, err := os.OpenRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var items []*item
	for _, n := range []string{"a.txt", "b.txt"} {
		fi, err := root.Lstat(n)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, &item{disk: n, root: root, rel: n, name: n, info: fi})
		os.Remove(n)
	}
	if err := syscall.Mkfifo("a.txt", 0o666); err != nil {
		t.Skip(err)
	}
	os.Symlink("/dev/zero", "b.txt")
	for i, want := range []string{"file was replaced", "escapes"} {
		done := make(chan error, 1)
		go func() {
			f, _, err := items[i].open()
			if f != nil {
				f.Close()
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: got %v, want %q", items[i].disk, err, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: open blocks", items[i].disk)
		}
	}
}

func TestFixKeepsMode(t *testing.T) {
	setup(t, map[string]string{"a.bin": randomData(20000, 5), "b.txt": "b"})
	uc2Run(t, 0, "ap", "prot", "a.bin", "b.txt")
	uc2Run(t, 0, "a", "plain", "a.bin", "b.txt")
	os.Chmod("prot.UC2", 0o600)
	os.Chmod("plain.UC2", 0o660)
	corrupt(t, "prot.UC2", 2000)  // repaired with the protection records
	corrupt(t, "plain.UC2", 5000) // salvaged
	for i, arch := range []string{"prot", "plain"} {
		uc2Run(t, sevDamaged, "t", arch)
		fix := []string{"FIX_0001.UC2", "FIX_0002.UC2"}[i]
		fi, err := os.Stat(fix)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := os.Stat(arch + ".UC2")
		if fi.Mode().Perm() != want.Mode().Perm() {
			t.Errorf("%s: mode %v, want %v", fix, fi.Mode(), want.Mode())
		}
	}
}

func TestRewriteKeepsOwner(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root")
	}
	setup(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	uc2Run(t, 0, "a", "arch", "a.txt", "b.txt")
	if err := os.Chown("arch.UC2", 1234, 5678); err != nil {
		t.Fatal(err)
	}
	uc2Run(t, 0, "d", "arch", "b.txt")
	fi, err := os.Stat("arch.UC2")
	if err != nil {
		t.Fatal(err)
	}
	if st := fi.Sys().(*syscall.Stat_t); st.Uid != 1234 || st.Gid != 5678 {
		t.Errorf("owner %d:%d", st.Uid, st.Gid)
	}
}
