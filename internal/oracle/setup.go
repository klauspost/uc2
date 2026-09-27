package oracle

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type download struct{ name, url, sha256 string }

var downloads = map[string]download{
	R2:   {"uc2r2.exe", "https://archive.org/download/uc2-rev-3-pro/uc2r2.exe", "cb699527b720e19ba925a344fdca08865347387d11e58d47736b7e4e345b858c"},
	V23:  {"uc2pro.exe", "https://www.sac.sk/download/pack/uc2pro.exe", "db5fd20ff3895bd6fea91869e8769e24549fe921a696dc5afcc205227622145b"},
	V237: {"uc237b.exe", "https://www.sac.sk/download/pack/uc237b.exe", "c286d96c104865f371b661ba5e46e34bd6f6437fc30606947ca34a62726c15ce"},
}

func cacheDir() (string, error) {
	d := os.Getenv("UC2_ORACLE_CACHE")
	if d == "" {
		c, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		d = filepath.Join(c, "uc2-oracle")
	}
	return d, os.MkdirAll(d, 0o755)
}

// prepare builds the cache. Every artifact is created under a temporary
// name and renamed into place, so concurrent test processes do not race.
func prepare() (*Env, error) {
	cache, err := cacheDir()
	if err != nil {
		return nil, err
	}
	exe, err := dosboxExe(cache, os.Getenv("UC2_DOSBOX"))
	if err != nil {
		return nil, fmt.Errorf("dosbox-x: %w", err)
	}
	e := &Env{
		dosbox: exe,
		dirs:   map[string]string{},
		errs:   map[string]error{},
		sem:    make(chan struct{}, max(1, runtime.NumCPU()/2)),
	}
	for _, v := range []string{R2, V23, V237} {
		if dir, err := e.install(cache, v); err != nil {
			e.errs[v] = err
		} else {
			e.dirs[v] = dir
		}
	}
	return e, nil
}

// dosboxExe returns src, or extracts dosbox-x.exe if src is a zip.
func dosboxExe(cache, src string) (string, error) {
	if !strings.EqualFold(filepath.Ext(src), ".zip") {
		_, err := os.Stat(src)
		return src, err
	}
	name := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	dst := filepath.Join(cache, name, "dosbox-x.exe")
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	zr, err := zip.OpenReader(src)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	var f *zip.File
	for _, zf := range zr.File {
		if path.Base(zf.Name) == "dosbox-x.exe" && (f == nil || strings.HasSuffix(zf.Name, "/mingw/dosbox-x.exe")) {
			f = zf
		}
	}
	if f == nil {
		return "", errors.New("no dosbox-x.exe in " + src)
	}
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	return dst, writeAtomic(dst, b)
}

func writeAtomic(dst string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err2 := f.Close(); err == nil {
		err = err2
	}
	if err == nil {
		err = os.Rename(f.Name(), dst)
	}
	if err != nil {
		os.Remove(f.Name())
		if _, serr := os.Stat(dst); serr == nil {
			return nil
		}
	}
	return err
}

// fetch returns the verified download for d.
func fetch(cache string, d download) ([]byte, error) {
	dst := filepath.Join(cache, "dl", d.name)
	if b, err := os.ReadFile(dst); err == nil && sum(b) == d.sha256 {
		return b, nil
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Get(d.url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", d.url, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if s := sum(b); s != d.sha256 {
		return nil, fmt.Errorf("%s: sha256 %s, want %s", d.url, s, d.sha256)
	}
	return b, writeAtomic(dst, b)
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// install returns a directory holding UC.EXE (and AIP-NL.INI) for v, ready
// to be copied to U:\ for each run.
func (e *Env) install(cache, v string) (string, error) {
	dst := filepath.Join(cache, "uc-"+v)
	if _, err := os.Stat(filepath.Join(dst, "UC.EXE")); err == nil {
		return dst, nil
	}
	b, err := fetch(cache, downloads[v])
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(cache, ".tmp-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	final := tmp
	if v == R2 {
		err = installR2(tmp, b)
	} else {
		final, err = e.installSEA(tmp, b)
	}
	if err != nil {
		return "", err
	}
	if err := os.Rename(final, dst); err != nil {
		if _, serr := os.Stat(filepath.Join(dst, "UC.EXE")); serr != nil {
			return "", err
		}
	}
	return dst, nil
}

// uc2r2.exe is UC.EXE followed by ALL.UC2 (the documentation) and an
// installer configuration. Like its installer we copy UC.EXE and point the
// configured paths at the directory of UC.EXE, which is U: in every run.
const (
	r2ExeSize = 133763
	confSize  = 357 // struct CONF, MAIN.H
	confMagic = 0xAC283746
)

func installR2(dir string, b []byte) error {
	exe := bytes.Clone(b[:r2ExeSize])
	c := exe[len(exe)-confSize:]
	qtrans(c)
	if binary.LittleEndian.Uint32(c[11:]) != confMagic || c[0] != 0 {
		return errors.New("uc2r2.exe: unexpected configuration block")
	}
	for i := range 4 { // pbTPATH, pcMan, pcLog, pcBat
		p := c[37+80*i : 37+80*(i+1)]
		clear(p)
		copy(p, "U:")
	}
	qtrans(c)
	return os.WriteFile(filepath.Join(dir, "UC.EXE"), exe, 0o644)
}

// qtrans (de)obfuscates CONF (QTrans in MAIN.CPP).
func qtrans(b []byte) {
	q := byte(0x27)
	for i := range b {
		b[i] ^= q
		q += byte(0x31 ^ i)
	}
}

// installSEA extracts a UC2SEA distribution and lets UC.EXE perform its
// first-run initialisation, which rewrites UC.EXE and AIP-NL.INI and
// resolves "?:" in AIP-NL.INI to U:. It returns the directory to keep.
func (e *Env) installSEA(tmp string, sea []byte) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	u := filepath.Join(tmp, "U")
	if err := os.Mkdir(u, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, "SEA.EXE"), sea, 0o644); err != nil {
		return "", err
	}
	if rc, out, err := e.dosboxRun(ctx, tmp, u, []string{"SEA UC2DIST"}); err != nil || rc != 0 {
		return "", fmt.Errorf("self-extractor: rc %d, %v\n%s", rc, err, out)
	}
	dist := filepath.Join(tmp, "UC2DIST")
	if rc, out, err := e.dosboxRun(ctx, u, dist, []string{"UC L NOTHERE"}); err != nil || !strings.Contains(out, "NOTHERE") {
		return "", fmt.Errorf("first run: rc %d, %v\n%s", rc, err, out)
	}
	keep := filepath.Join(tmp, "keep")
	if err := os.Mkdir(keep, 0o755); err != nil {
		return "", err
	}
	for _, n := range []string{"UC.EXE", "AIP-NL.INI"} {
		if err := os.Rename(filepath.Join(dist, n), filepath.Join(keep, n)); err != nil {
			return "", err
		}
	}
	return keep, nil
}
