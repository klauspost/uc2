package oracle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOracleRun(t *testing.T) {
	e := New(t)
	for _, v := range e.Versions() {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			w := t.TempDir()
			if err := os.WriteFile(filepath.Join(w, "A.TXT"), []byte(strings.Repeat("hello oracle\r\n", 100)), 0o644); err != nil {
				t.Fatal(err)
			}
			rc, out, err := e.Run(context.Background(), v, w, "UC A T A.TXT", "UC T T")
			if err != nil || rc != 0 || !strings.Contains(out, "Verifying A.TXT") {
				t.Fatalf("rc %d, err %v\n%s", rc, err, out)
			}
			if _, err := os.Stat(filepath.Join(w, "T.UC2")); err != nil {
				t.Fatal(err)
			}
			rc, out, err = e.Run(context.Background(), v, w, "UC T NOPE", "UC T T")
			if err != nil || rc != 130 {
				t.Fatalf("missing archive: rc %d, err %v\n%s", rc, err, out)
			}
			ents, _ := os.ReadDir(w)
			if len(ents) != 2 {
				t.Errorf("work dir polluted: %v", ents)
			}
		})
	}
}
