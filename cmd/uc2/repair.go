package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"runtime"
	"slices"

	"github.com/klauspost/uc2"
	"github.com/klauspost/uc2/internal/dp"
	"github.com/klauspost/uc2/internal/format"
)

const sectorSize = 512

// test verifies an archive. Damage is repaired into FIX_nnnn.UC2, first with
// the damage protection records, otherwise by salvaging the intact files.
// The original is never modified.
func (a *app) test(c *cmd, arch string) error {
	a.header("Testing", c, arch)
	r, err := uc2.OpenReader(arch, c.opts(nil)...)
	if err != nil {
		if !damaged(err) {
			return archiveErr(arch, err)
		}
		a.errorf(sevDamaged, "archive %s is damaged (%v)", arch, err)
		return a.repair(c, arch, nil, nil)
	}
	defer r.Close()
	checked := a.check(&r.Reader, arch)
	if !checked && r.Protected {
		return a.repair(c, arch, &r.Reader, nil)
	}
	bad, err := a.verify(c, &r.Reader)
	if err != nil || checked && len(bad) == 0 {
		return err
	}
	return a.repair(c, arch, &r.Reader, bad)
}

func (a *app) check(r *uc2.Reader, arch string) bool {
	err := r.Check()
	switch {
	case err != nil:
		a.errorf(sevDamaged, "archive %s is damaged (%v)", arch, err)
	case r.Protected:
		a.printf("Testing archive sectors OK\nTesting protection records OK\n")
	}
	if !r.Protected {
		a.printf("Archive is not damage protected\n")
	}
	return err == nil
}

// verify decompresses all file revisions in parallel and reports them in
// archive order. It returns the damaged revisions.
func (a *app) verify(c *cmd, r *uc2.Reader) (map[*uc2.File]bool, error) {
	var files []*uc2.File
	for _, f := range r.File {
		if !isDir(f) {
			files = append(files, f)
		}
	}
	errs := make([]error, len(files))
	done := make([]chan struct{}, len(files))
	for i := range done {
		done[i] = make(chan struct{})
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		sem := make(chan struct{}, cmp.Or(c.threads, runtime.GOMAXPROCS(0)))
		for i, f := range files {
			select {
			case sem <- struct{}{}:
			case <-stop:
				return
			}
			go func() {
				defer func() { <-sem; close(done[i]) }()
				errs[i] = verifyFile(f)
			}()
		}
	}()
	bad := map[*uc2.File]bool{}
	for i, f := range files {
		<-done[i]
		switch err := errs[i]; {
		case errors.Is(err, errors.ErrUnsupported):
			return nil, fatalf(sevVersion, "%s: %v", dispFile(f), err)
		case err != nil:
			bad[f] = true
			a.errorf(sevDamaged, "file %s is damaged", dispFile(f))
		default:
			a.printf("Verifying %s OK\n", dispFile(f))
		}
	}
	return bad, nil
}

func verifyFile(f *uc2.File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc)
	return err
}

// repair creates FIX_nnnn.UC2. r is nil if the archive cannot be read; bad
// is nil if the files of r have not been verified.
func (a *app) repair(c *cmd, arch string, r *uc2.Reader, bad map[*uc2.File]bool) error {
	// The copy is never more accessible than the original.
	perm := fs.FileMode(0o600)
	if fi, err := os.Stat(arch); err == nil {
		perm = fi.Mode().Perm()
	}
	fix, err := a.repairSectors(arch, perm)
	if err != nil {
		return err
	}
	src, srcBad := r, bad
	var fr *uc2.ReadCloser
	if fix != "" {
		a.printf("\nTesting/repairing %s\n", fix)
		if fr, err = uc2.OpenReader(fix, c.opts(nil)...); err != nil {
			a.errorf(sevDamaged, "archive %s is damaged (%v)", fix, err)
		} else {
			defer fr.Close()
			checked := a.check(&fr.Reader, fix)
			fb, err := a.verify(c, &fr.Reader)
			if err != nil {
				return err
			}
			if checked && len(fb) == 0 {
				a.printf(" MESSAGE: all files have been restored 100%%\n")
				a.untouched(fix, arch)
				return nil
			}
			src, srcBad = &fr.Reader, fb
		}
	}
	if src == nil {
		a.errorf(sevDamaged, "archive %s cannot be repaired", arch)
		return nil
	}
	if srcBad == nil {
		if srcBad, err = a.verify(c, src); err != nil {
			return err
		}
	}
	name, err := a.salvage(c, src, srcBad, perm)
	if err != nil {
		return err
	}
	msg := "some files might be damaged"
	if fix != "" {
		// Replace the sector repaired copy, which is still damaged.
		if fr != nil {
			fr.Close()
		}
		if os.Remove(fix) == nil && os.Rename(name, fix) == nil {
			name = fix
		}
		msg = "some files might still be damaged"
	}
	if len(srcBad) == 0 {
		msg = "all files have been restored 100%"
	}
	a.printf(" MESSAGE: %s\n", msg)
	a.untouched(name, arch)
	return nil
}

func (a *app) untouched(fix, arch string) {
	a.printf(" MESSAGE: original archive is not touched (beware of bad sectors!)\n")
	a.printf("          (%s contains a repaired 'copy' of %s)\n", fix, arch)
}

// claimFix creates the first free FIX_nnnn.UC2 in the current directory.
func claimFix(perm fs.FileMode) (*os.File, error) {
	for n := 1; n <= 9999; n++ {
		f, err := os.OpenFile(fmt.Sprintf("FIX_%04d.UC2", n), os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)
		if !errors.Is(err, fs.ErrExist) {
			if err != nil {
				break
			}
			if runtime.GOOS != "windows" {
				f.Chmod(perm) // undo the umask
			}
			return f, nil
		}
	}
	return nil, fatalf(sevFix, "cannot create a FIX_????.UC2 file")
}

// repairSectors reconstructs damaged sectors with the damage protection
// records into a FIX file. It returns "" if that is not possible.
func (a *app) repairSectors(arch string, perm fs.FileMode) (string, error) {
	f, err := os.Open(arch)
	if err != nil {
		return "", archiveErr(arch, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", archiveErr(arch, err)
	}
	size := fi.Size()
	var head [format.FHeadSize]byte
	f.ReadAt(head[:], 0)
	fh, err := format.ParseFHead(head[:])
	if err != nil {
		f.ReadAt(head[:], size-format.FHeadSize)
		if fh, err = format.ParseFHead(head[:]); err != nil {
			return "", nil
		}
	}
	if !fh.Protected {
		return "", nil
	}
	l := protectedLen(fh, size)
	res, err := dp.Verify(f, l)
	if err != nil || len(res.Bad) == 0 {
		return "", nil // intact sectors: damage elsewhere is salvaged
	}
	fixed, err := dp.Repair(f, l, res)
	if err != nil {
		a.errorf(sevDamaged, "failed to repair archive (using damage protection)")
		return "", nil
	}
	out, err := claimFix(perm)
	if err != nil {
		return "", err
	}
	name := out.Name()
	a.printf("Creating archive %s\n", name)
	_, err = io.Copy(out, io.NewSectionReader(f, 0, size))
	for _, s := range slices.Sorted(maps.Keys(fixed)) {
		if err == nil {
			_, err = out.WriteAt(fixed[s], s*sectorSize)
		}
		a.printf("Reconstructing sector %d OK\n", s+1)
	}
	if err == nil {
		// The spare header after the protection area is not covered by it.
		if _, err = out.ReadAt(head[:], 0); err == nil {
			_, err = out.WriteAt(head[:], l+dp.AreaSize(l))
		}
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return "", fatalf(sevFix, "cannot write %s (%v)", name, err)
	}
	a.printf("Archive has been repaired (using damage protection)\n")
	return name, nil
}

// protectedLen returns the archive length covered by damage protection.
// FHEAD stores it modulo 2^32 in extended archives; the file size decides.
func protectedLen(fh format.FHead, size int64) int64 {
	l0 := format.FHeadSize + int64(fh.CompLen)
	for l := l0; l < size; l += 1 << 32 {
		if l+dp.AreaSize(l)+format.FHeadSize == size {
			return l
		}
	}
	return l0
}

// salvage copies every intact entry of src into a new FIX file.
func (a *app) salvage(c *cmd, src *uc2.Reader, bad map[*uc2.File]bool, perm fs.FileMode) (string, error) {
	out, err := claimFix(perm)
	if err != nil {
		return "", err
	}
	name := out.Name()
	a.printf("Creating archive %s\n", name)
	protected := src.Protected
	w := uc2.NewWriter(out, c.opts(&protected)...)
	if src.Label != "" {
		err = w.SetLabel(src.Label)
	}
	for _, f := range src.File {
		if err == nil && !bad[f] {
			err = w.Copy(f)
		}
	}
	if err == nil {
		err = w.Close()
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return "", fatalf(sevFix, "cannot write %s (%v)", name, err)
	}
	return name, nil
}
