package super

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/klauspost/uc2/internal/format"
)

func TestData(t *testing.T) {
	h := sha256.Sum256([]byte(Data))
	if len(Data) != 49152 || hex.EncodeToString(h[:]) != "4f2e3fb48a288f66a76b0e26cb2830f3f0297f3686752853de65b82e646e840e" {
		t.Fatal("supermaster mismatch")
	}
	if f := format.Fletch([]byte(Data)); f != 0x1E55 {
		t.Fatalf("fletcher %04x", f)
	}
}
