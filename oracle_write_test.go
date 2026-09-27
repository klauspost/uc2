package uc2

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/uc2/internal/oracle"
)

// goCorpus extends the oracle corpus with long names, revisions and files
// large enough to be compressed in several fragments.
func goCorpus() []oracleFile {
	fs := oracleCorpus()
	text := bytes.Repeat(fs[2].data, 1+2300000/len(fs[2].data))[:2300000]
	random := make([]byte, 1500000)
	for i := range random {
		random[i] = byte(uint32(i)*2654435761>>13 ^ uint32(i)>>7)
	}
	// Incompressible data is coded in blocks larger than UC2 writes.
	noise := make([]byte, 400000)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range noise {
		noise[i] = byte(rng.Uint32())
	}
	add := func(name string, data []byte) { fs = append(fs, oracleFile{name, data, oracleEpoch}) }
	add("A long file name.txt", []byte("long name\r\n"))
	add("lower.txt", []byte("lower case 8.3 name\r\n"))
	add("Café crème.txt", []byte("CP437 long name\r\n"))
	add("Long Directory Name/Another Long Name.dat", []byte("inside a long directory\r\n"))
	add("BIG/TEXT.TXT", text)
	add("BIG/RANDOM.BIN", random)
	add("BIG/NOISE.BIN", noise)
	add("BIG/SOUND.WAV", oracleWAV(700000))
	for i := range 300 {
		add(fmt.Sprintf("MANY/F%04d.TXT", i), fmt.Appendf(nil, "file %d\r\n%s", i, fs[2].data[:i*5]))
	}
	for i := 1; i <= 3; i++ {
		fs = append(fs, oracleFile{"VERS.TXT", fmt.Appendf(nil, "version %d\r\n%s", i, bytes.Repeat([]byte("same "), 30*i)), oracleEpoch.Add(time.Duration(i) * time.Hour)})
	}
	return fs
}

func writeGoArchive(t testing.TB, files []oracleFile, opts ...Option) []byte {
	var m memFile
	w := NewWriter(&m, opts...)
	for _, f := range files {
		fw, err := w.CreateHeader(&FileHeader{Name: f.name, Modified: f.mod, Attr: AttrArchive})
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(f.data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return m.b
}

// hostPaths maps the newest revision of every file in b to the relative host
// path UC2 extracts it to: 8.3 names, or long names when longNames is set.
func hostPaths(t *testing.T, b []byte, longNames bool) map[string]string {
	r, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	short := map[string]string{"": ""}
	m := map[string]string{}
	for _, f := range r.File {
		if f.Revision != 0 || strings.HasPrefix(f.ShortName, "U$~") {
			continue
		}
		name := strings.TrimSuffix(f.Name, "/")
		parent := short[path.Dir(name)]
		if path.Dir(name) == "." {
			parent = ""
		}
		elem := f.ShortName
		if longNames {
			elem = path.Base(name)
		}
		p := path.Join(parent, elem)
		if strings.HasSuffix(f.Name, "/") {
			short[name] = p
		} else {
			m[f.Name] = p
		}
	}
	return m
}

func TestOracleGoToUC2(t *testing.T) {
	e := oracle.New(t)
	ctx := context.Background()
	files := goCorpus()
	newest := map[string][]byte{}
	for _, f := range files {
		newest[f.name] = f.data
	}
	for _, a := range []struct {
		name string
		opts []Option
	}{
		{"TF", []Option{WithLevel(Fast)}},
		{"TN", nil},
		{"TT", []Option{WithLevel(Tight)}},
		{"TST", []Option{WithLevel(SuperTight)}},
		{"PROT", []Option{WithDamageProtection(true)}},
	} {
		data := writeGoArchive(t, files, a.opts...)
		verifyOracleArchive(t, data, files)
		// UC 2.37b hangs restoring a long name whose stored 8.3 alias differs
		// from the one the file system generates, which is unavoidable for
		// non-ASCII names under DOSBox-X (it does not upper-case them).
		var ascii []oracleFile
		for _, f := range files {
			if isASCII(f.name) {
				ascii = append(ascii, f)
			}
		}
		data237 := writeGoArchive(t, ascii, a.opts...)
		for _, v := range e.Versions() {
			t.Run(a.name+"/"+v, func(t *testing.T) {
				t.Parallel()
				data := data
				if v == oracle.V237 {
					data = data237
				}
				w := t.TempDir()
				os.WriteFile(filepath.Join(w, "A.UC2"), data, 0o644)
				rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
				defer cancel()
				rc, out, err := e.Run(rctx, v, w, `UC T -F A`, `md X`, `cd X`, `UC E -S -F ..\A`)
				if err != nil || rc != 0 {
					t.Fatalf("rc %d, %v\n%s", rc, err, out)
				}
				for name, p := range hostPaths(t, data, v == oracle.V237) {
					got, err := os.ReadFile(filepath.Join(w, "X", filepath.FromSlash(p)))
					if err != nil || !bytes.Equal(got, newest[name]) {
						t.Errorf("%s (%s): %v, %d bytes, want %d", name, p, err, len(got), len(newest[name]))
					}
				}
			})
		}
	}

	// Names DOS cannot store directly: they must still pass UC2's test.
	odd := []oracleFile{
		{"con.txt", []byte("device name"), oracleEpoch},
		{"日本語.txt", []byte("utf-8 name"), oracleEpoch},
		{"a:b.txt", []byte("colon"), oracleEpoch},
		{"U$~BAN.TXT", []byte("not a banner"), oracleEpoch},
	}
	oddData := writeGoArchive(t, odd)
	verifyOracleArchive(t, oddData, odd)
	for _, v := range e.Versions() {
		t.Run("names/"+v, func(t *testing.T) {
			t.Parallel()
			w := t.TempDir()
			os.WriteFile(filepath.Join(w, "A.UC2"), oddData, 0o644)
			if rc, out, err := e.Run(ctx, v, w, `UC T -F A`); err != nil || rc != 0 {
				t.Fatalf("rc %d, %v\n%s", rc, err, out)
			}
		})
	}

	// UC2 updates our archive: basic add (reusing our *.TXT master), incremental
	// add, delete, protect and unprotect. Our reader must see the result.
	base := writeGoArchive(t, files)
	for _, v := range e.Versions() {
		t.Run("update/"+v, func(t *testing.T) {
			t.Parallel()
			w := t.TempDir()
			os.WriteFile(filepath.Join(w, "B.UC2"), base, 0o644)
			add := []oracleFile{
				{"NEWFILE.TXT", bytes.Repeat([]byte("the quick brown fox "), 400), oracleEpoch},
				{"NEW2.TXT", []byte("incremental\r\n"), oracleEpoch},
			}
			writeOracleFiles(t, w, add...)
			rc, out, err := e.Run(ctx, v, w, `UC A -F B NEWFILE.TXT`, `UC A -I -F B NEW2.TXT`, `UC D -F B ZEROS.BIN`,
				`UC P -F B`, `UC T -F B`, `UC U -F B`, `UC T -F B`)
			if err != nil || rc != 0 {
				t.Fatalf("rc %d, %v\n%s", rc, err, out)
			}
			b, _ := os.ReadFile(filepath.Join(w, "B.UC2"))
			var want []oracleFile
			for _, f := range files {
				if f.name != "ZEROS.BIN" {
					want = append(want, f)
				}
			}
			verifyOracleArchive(t, b, append(want, add...))
			// And our in-place update of their result.
			m := &memFile{b: b}
			if err := appendFiles(t, m, int64(len(b))); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(w, "B.UC2"), m.b, 0o644)
			if rc, out, err := e.Run(ctx, v, w, `UC T -F B`); err != nil || rc != 0 {
				t.Fatalf("after our append: rc %d, %v\n%s", rc, err, out)
			}
		})
	}
}
