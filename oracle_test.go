package uc2

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/uc2/internal/oracle"
)

const oracleDir = "testdata/oracle"

// oracleEpoch is the mtime of all corpus files. It has even seconds, as DOS times do.
var oracleEpoch = time.Date(2025, 1, 2, 3, 4, 6, 0, time.Local)

type oracleFile struct {
	name string // slash separated
	data []byte
	mod  time.Time
}

func oracleCorpus() []oracleFile {
	rng := rand.New(rand.NewPCG(1, 2))
	words := strings.Fields("the quick brown fox jumps over a lazy dog while ultra compressor " +
		"packs every archive with masters revisions delta huffman trees and damage protection")
	var text bytes.Buffer
	for text.Len() < 2000 {
		for n := 0; n < 70; {
			w := words[rng.IntN(len(words))]
			text.WriteString(w + " ")
			n += len(w) + 1
		}
		text.WriteString("\r\n")
	}
	var src bytes.Buffer
	for i := range 25 {
		fmt.Fprintf(&src, "int func%02d(int a, int b)\r\n{\r\n\treturn a * %d + (b >> %d);\r\n}\r\n\r\n", i, i*7+1, i%5)
	}
	bin := make([]byte, 2048)
	for i := range 512 {
		binary.LittleEndian.PutUint32(bin[i*4:], uint32(i*i)^0x5A5A0000)
	}
	random := make([]byte, 1024)
	for i := range random {
		random[i] = byte(rng.Uint32())
	}
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	fs := []oracleFile{
		{name: "HELLO.TXT", data: []byte("Hello, UltraCompressor II!\r\n")},
		{name: "EMPTY.DAT"},
		{name: "TEXT.TXT", data: text.Bytes()},
		{name: "SOURCE.C", data: src.Bytes()},
		{name: "BINARY.BIN", data: bin},
		{name: "RANDOM.BIN", data: random},
		{name: "ZEROS.BIN", data: make([]byte, 8192)},
		{name: "ALLBYTES.BIN", data: all},
		{name: "SOUND.WAV", data: oracleWAV(2000)},
		{name: "SUB/NESTED.TXT", data: []byte("nested file\r\n")},
		{name: "SUB/DEEP/DEEPER.TXT", data: bytes.Repeat([]byte("deeper "), 50)},
	}
	for i := range fs {
		fs[i].mod = oracleEpoch
	}
	return fs
}

// oracleWAV returns a 16-bit mono PCM WAV file, which TT/TST compress with delta coding.
func oracleWAV(samples int) []byte {
	b := make([]byte, 44+2*samples)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 11025)
	binary.LittleEndian.PutUint32(b[28:], 2*11025)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*samples))
	for i := range samples {
		x := 9000*math.Sin(2*math.Pi*float64(i)/50) + 2500*math.Sin(2*math.Pi*float64(i)/7.3)
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(x)))
	}
	return b
}

func writeOracleFiles(t testing.TB, dir string, fs ...oracleFile) {
	t.Helper()
	dirs := map[string]bool{}
	for _, f := range fs {
		for d := path.Dir(f.name); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
		p := filepath.Join(dir, filepath.FromSlash(f.name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.data, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, f.mod, f.mod); err != nil {
			t.Fatal(err)
		}
	}
	for d := range dirs {
		if err := os.Chtimes(filepath.Join(dir, filepath.FromSlash(d)), oracleEpoch, oracleEpoch); err != nil {
			t.Fatal(err)
		}
	}
}

// oracleArchive is an archive made by the original UC2.
type oracleArchive struct {
	name string // DOS base name in the work dir
	ver  string
	how  []string
	want []oracleFile // all revisions, oldest first
	data []byte
}

func (a *oracleArchive) file() string {
	v := "v" + strings.ReplaceAll(a.ver, ".", "")
	if a.ver == oracle.R2 {
		v = "r2"
	}
	return strings.ToLower(v + "_" + a.name + ".uc2")
}

func TestOracleUC2ToGo(t *testing.T) {
	e := oracle.New(t)
	ctx := context.Background()
	corpus := oracleCorpus()
	var mu sync.Mutex
	var made []*oracleArchive
	for _, v := range e.Versions() {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			w := t.TempDir()
			run := func(cmds ...string) {
				t.Helper()
				rc, out, err := e.Run(ctx, v, w, cmds...)
				if err != nil || rc != 0 {
					t.Fatalf("%q: rc %d, %v\n%s", cmds, rc, err, out)
				}
			}
			var archs []*oracleArchive
			add := func(name, dir string, want []oracleFile, cmds ...string) {
				run(append([]string{"cd " + dir}, cmds...)...)
				archs = append(archs, &oracleArchive{name: name, ver: v, how: cmds, want: want})
			}

			writeOracleFiles(t, filepath.Join(w, "CORPUS"), corpus...)
			var root []oracleFile
			for _, f := range corpus {
				if !strings.Contains(f.name, "/") {
					root = append(root, f)
				}
			}
			add("DEF", "CORPUS", root, `UC A C:\DEF *.*`)
			for _, o := range []string{"TF", "TN", "TT", "TST"} {
				add(o, "CORPUS", corpus, fmt.Sprintf(`UC A -S -%s C:\%s *.*`, o, o))
			}
			add("PROT", "CORPUS", corpus, `UC A -S -P C:\PROT *.*`)

			// Revisions: VERS.TXT changes twice, NEW.TXT appears in the second update,
			// ÉTÉ.TXT is unchanged and keeps one revision.
			rev := func(n int) oracleFile {
				return oracleFile{"VERS.TXT", fmt.Appendf(nil, "version %d\r\n%s", n, bytes.Repeat([]byte("same text "), 20*n)), oracleEpoch.Add(time.Duration(n) * time.Hour)}
			}
			keep := oracleFile{"ÉTÉ.TXT", []byte("unchanged, CP437 name\r\n"), oracleEpoch}
			newf := oracleFile{"NEW.TXT", []byte("added later\r\n"), oracleEpoch}
			revCmds := []string{`UC A C:\REV *.*`, `UC A -I C:\REV *.*`, `UC A -I C:\REV *.*`}
			writeOracleFiles(t, filepath.Join(w, "REVS"), rev(1), keep)
			run("cd REVS", revCmds[0])
			writeOracleFiles(t, filepath.Join(w, "REVS"), rev(2), newf)
			run("cd REVS", revCmds[1])
			writeOracleFiles(t, filepath.Join(w, "REVS"), rev(3))
			run("cd REVS", revCmds[2])
			archs = append(archs, &oracleArchive{name: "REV", ver: v, how: revCmds, want: []oracleFile{rev(1), keep, rev(2), newf, rev(3)}})

			if v == oracle.V237 {
				long := []oracleFile{
					{"A long file name.txt", []byte("long name\r\n"), oracleEpoch},
					{"lower.txt", []byte("lower case 8.3 name\r\n"), oracleEpoch},
					{"multi.part.name.txt", []byte("dots\r\n"), oracleEpoch},
					{"UPPER.TXT", []byte("plain 8.3 name\r\n"), oracleEpoch},
					{"Café crème.txt", []byte("CP437 long name\r\n"), oracleEpoch},
					{"Long Directory Name/Another Long Name.dat", []byte("inside a long directory\r\n"), oracleEpoch},
				}
				writeOracleFiles(t, filepath.Join(w, "LONG"), long...)
				add("LFN", "LONG", long, `UC A -S C:\LFN *.*`)
			}

			for _, a := range archs {
				b, err := os.ReadFile(filepath.Join(w, a.name+".UC2"))
				if err != nil {
					t.Fatal(err)
				}
				a.data = b
			}
			mu.Lock()
			made = append(made, archs...)
			mu.Unlock()

			for _, a := range archs {
				t.Run(a.name, func(t *testing.T) {
					t.Parallel()
					verifyOracleArchive(t, a.data, a.want)
					// Every version, including r2, accepts archives made by the others.
					for _, tv := range e.Versions() {
						tw := t.TempDir()
						if err := os.WriteFile(filepath.Join(tw, "A.UC2"), a.data, 0o644); err != nil {
							t.Fatal(err)
						}
						if rc, out, err := e.Run(ctx, tv, tw, `UC T -F A`); err != nil || rc != 0 {
							t.Errorf("UC %s T: rc %d, %v\n%s", tv, rc, err, out)
						}
					}
				})
			}
		})
	}
	if os.Getenv("UC2_ORACLE_SAVE") != "" {
		t.Cleanup(func() {
			if !t.Failed() {
				saveOracle(t, made)
			}
		})
	}
}

// verifyOracleArchive checks every revision of every file against want.
func verifyOracleArchive(t *testing.T, data []byte, want []oracleFile) {
	t.Helper()
	r, err := NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Check(); err != nil {
		if r.Protected {
			t.Logf("check (protected archive): %v", err)
		} else {
			t.Error("check:", err)
		}
	}
	revs := map[string][]oracleFile{}
	dirs := map[string]bool{}
	for _, f := range want {
		revs[f.name] = append(revs[f.name], f)
		for d := path.Dir(f.name); d != "."; d = path.Dir(d) {
			dirs[d+"/"] = true
		}
	}
	seen := map[string]int{}
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, "/") {
			if !dirs[f.Name] {
				t.Errorf("unexpected directory %q", f.Name)
			}
			delete(dirs, f.Name)
			continue
		}
		exp := revs[f.Name]
		i := len(exp) - 1 - f.Revision
		if i < 0 {
			t.Errorf("unexpected file %q revision %d", f.Name, f.Revision)
			continue
		}
		seen[f.Name]++
		rc, err := f.Open()
		if err != nil {
			t.Errorf("%s;%d: %v", f.Name, f.Revision, err)
			continue
		}
		got, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Errorf("%s;%d: %v", f.Name, f.Revision, err)
		} else if !bytes.Equal(got, exp[i].data) {
			t.Errorf("%s;%d: content mismatch (%d bytes, want %d)", f.Name, f.Revision, len(got), len(exp[i].data))
		}
		if f.Size != int64(len(exp[i].data)) {
			t.Errorf("%s;%d: size %d, want %d", f.Name, f.Revision, f.Size, len(exp[i].data))
		}
		if !dosTimeMatch(f.Modified, exp[i].mod) {
			t.Errorf("%s;%d: modified %v, want %v", f.Name, f.Revision, f.Modified, exp[i].mod)
		}
	}
	for name, exp := range revs {
		if seen[name] != len(exp) {
			t.Errorf("%s: %d revisions, want %d", name, seen[name], len(exp))
		}
	}
	for d := range dirs {
		t.Errorf("missing directory %q", d)
	}
}

// dosTimeMatch reports whether got is the DOS time of a host file with mtime want.
// DOSBox-X on Windows converts with the current UTC offset rather than the
// one in effect at want, so both are accepted.
func dosTimeMatch(got, want time.Time) bool {
	_, off := time.Now().Zone()
	return got.Equal(want) || got.Format(time.DateTime) == want.In(time.FixedZone("", off)).Format(time.DateTime)
}

// saveOracle writes the archives, a README and SHA-256 sums of every revision to testdata/oracle.
func saveOracle(t *testing.T, made []*oracleArchive) {
	slices.SortFunc(made, func(a, b *oracleArchive) int { return strings.Compare(a.file(), b.file()) })
	if err := os.MkdirAll(oracleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old, _ := filepath.Glob(filepath.Join(oracleDir, "*.uc2"))
	for _, f := range old {
		os.Remove(f)
	}
	var readme, sums strings.Builder
	readme.WriteString(`Archives made by the original UltraCompressor II in DOSBox-X by
TestOracleUC2ToGo (UC2_DOSBOX=... UC2_ORACLE_SAVE=1 go test -run OracleUC2ToGo).
The corpus is generated by oracleCorpus in oracle_test.go; all files have
mtime 2025-01-02 03:04:06 local time (VERS.TXT revision n: +n hours).
Commands run in the source directory with UC2_ANONYMOUS=1, UC2_OK=OFF.
sums.txt lists "archive sha256[:8] revision name" for every file revision
(names UTF-8, decoded from CP437). "UC T -F" of all three versions accepts
every archive. DOSBox-X on Windows converts mtimes to DOS time with the
current UTC offset, so stored times may differ by the DST offset. Re-running
with the same UTC offset reproduces the archives byte for byte.

Versions: r2 = UC2 revision 2 (uc2r2.exe), v23 = UC2 PRO 2.3 (uc2pro.exe),
v237b = UC 2.37 beta (uc237b.exe, stores Win95 long names).

`)
	for _, a := range made {
		if err := os.WriteFile(filepath.Join(oracleDir, a.file()), a.data, 0o644); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&readme, "%-16s %6d bytes  UC %s: %s\n", a.file(), len(a.data), a.ver, strings.Join(a.how, " ; "))
		count := map[string]int{}
		for _, f := range a.want {
			count[f.name]++
		}
		for _, f := range a.want {
			count[f.name]--
			fmt.Fprintf(&sums, "%s %s %d %s\n", a.file(), oracleSum(f.data), count[f.name], f.name)
		}
	}
	for name, s := range map[string]string{"README.txt": readme.String(), "sums.txt": sums.String()} {
		if err := os.WriteFile(filepath.Join(oracleDir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func oracleSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8])
}

// TestOracleArchives verifies the saved UC2-made archives without DOSBox.
func TestOracleArchives(t *testing.T) {
	f, err := os.Open(filepath.Join(oracleDir, "sums.txt"))
	if err != nil {
		t.Skip("no oracle archives")
	}
	defer f.Close()
	type key struct {
		name string
		rev  int
	}
	want := map[string]map[key]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.SplitN(sc.Text(), " ", 4)
		if len(p) != 4 {
			t.Fatalf("bad sums line %q", sc.Text())
		}
		rev, _ := strconv.Atoi(p[2])
		if want[p[0]] == nil {
			want[p[0]] = map[key]string{}
		}
		want[p[0]][key{p[3], rev}] = p[1]
	}
	for _, arch := range slices.Sorted(maps.Keys(want)) {
		t.Run(arch, func(t *testing.T) {
			r, err := OpenReader(filepath.Join(oracleDir, arch))
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if err := r.Check(); err != nil && !r.Protected {
				t.Error("check:", err)
			}
			left := maps.Clone(want[arch])
			for _, f := range r.File {
				if strings.HasSuffix(f.Name, "/") {
					continue
				}
				rc, err := f.Open()
				if err != nil {
					t.Fatal(f.Name, err)
				}
				b, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					t.Fatalf("%s;%d: %v", f.Name, f.Revision, err)
				}
				k := key{f.Name, f.Revision}
				if got := oracleSum(b); got != left[k] {
					t.Errorf("%s;%d: sha256 %s, want %q", f.Name, f.Revision, got, left[k])
				}
				delete(left, k)
			}
			for k := range left {
				t.Errorf("missing %s;%d", k.name, k.rev)
			}
		})
	}
}
