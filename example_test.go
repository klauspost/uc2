package uc2_test

import (
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/klauspost/uc2"
)

func ExampleNewWriter() {
	dir, _ := os.MkdirTemp("", "uc2")
	defer os.RemoveAll(dir)
	f, err := os.Create(filepath.Join(dir, "EXAMPLE.UC2"))
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	w := uc2.NewWriter(f, uc2.WithLevel(uc2.Tight))
	for name, body := range map[string]string{
		"readme.txt":         "This archive was written by Go.",
		"docs/long name.txt": "UC2 stores long names like UC 2.37b.",
	} {
		fw, err := w.Create(name)
		if err != nil {
			log.Fatal(err)
		}
		io.WriteString(fw, body)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	r, err := uc2.OpenReader(f.Name())
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()
	for _, f := range r.File {
		fmt.Printf("%-20s %-12s %d bytes\n", f.Name, f.ShortName, f.Size)
	}
	// Unordered output:
	// docs/                DOCS         0 bytes
	// readme.txt           README.TXT   31 bytes
	// docs/long name.txt   LONGNA~1.TXT 36 bytes
}

func ExampleReader_Open() {
	r, err := uc2.OpenReader("testdata/samples/UC2INFO.UC2")
	if err != nil {
		return
	}
	defer r.Close()
	fs.WalkDir(r, ".", func(path string, d fs.DirEntry, err error) error {
		fmt.Println(path)
		return err
	})
}
