//go:build ignore

// Fetch downloads the third-party test archives listed in samples.txt into testdata/samples.
// Run from the module root: go run testdata/fetch.go
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	f, err := os.Open("testdata/samples.txt")
	check(err)
	defer f.Close()
	check(os.MkdirAll("testdata/samples", 0o755))
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) != 3 {
			continue
		}
		sum, name, url := fields[0], fields[1], fields[2]
		dst := filepath.Join("testdata", "samples", name)
		if b, err := os.ReadFile(dst); err == nil && hash(b) == sum {
			continue
		}
		fmt.Println("fetching", name)
		resp, err := http.Get(url)
		check(err)
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		check(err)
		if resp.StatusCode != http.StatusOK || hash(b) != sum {
			check(fmt.Errorf("%s: status %s, sha256 %s, want %s", name, resp.Status, hash(b), sum))
		}
		check(os.WriteFile(dst, b, 0o644))
	}
	check(s.Err())
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
