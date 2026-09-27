package main

import (
	"os"
	"strings"
	"testing"
)

// TestTranscripts compares whole outputs with the layout of the original,
// as captured from UC2 revision 2 with redirected output (UC2 names shown
// upper case there). Errors and warnings go to stderr instead of stdout.
func TestTranscripts(t *testing.T) {
	setup(t, map[string]string{"README.TXT": randomData(3200, 8), "DATA.BIN": randomData(20000, 7), "SUB/NOTE.TXT": "note"})
	logo := func(out string) string {
		t.Helper()
		_, rest, ok := strings.Cut(out, "\n\n")
		if !ok || !strings.HasPrefix(out, "═") {
			t.Fatalf("no logo:\n%s", out)
		}
		return rest
	}
	for _, tc := range []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"A", "T", "README.TXT", "DATA.BIN"}, 0, `Adding files to T.UC2
Scanning␠
Analyzing␠
Compressing README.TXT DONE
Compressing DATA.BIN DONE
Updated archive contains 2 files (23,200 bytes)␠␠
Optimizing compression␠

Everything went OK
`},
		{[]string{"AS", "T", `SUB\*.*`}, 0, `Adding files to T.UC2
Scanning␠
Analyzing␠
Compressing SUB\NOTE.TXT DONE
Updated archive contains 3 files (23,204 bytes)␠␠
Optimizing compression␠

Everything went OK
`},
		{[]string{"A", "T", "DATA.BIN"}, 0, `Adding files to T.UC2
Scanning␠
Smart skipping 20,000 bytes
Analyzing␠
Updated archive contains 3 files (23,204 bytes)␠␠

Everything went OK
`},
		{[]string{"A", "T", "NOPE.XYZ"}, sevNoMatch, `Adding files to T.UC2
Scanning␠
Analyzing␠
Updated archive contains 3 files (23,204 bytes)␠␠
 WARNING 20: no file found matching NOPE.XYZ

1 warning has been reported␠
`},
		{[]string{"L", "T"}, 0, `Listing files from T.UC2
--> Directory of \
README.TXT      DATA.BIN        NOTE.TXT       ␠
files listed = 3 (23,204 bytes)

Everything went OK
`},
		{[]string{"L", "NOPE"}, sevNoArchive, `Listing files from NOPE.UC2

FATAL ERROR 130: NOPE.UC2 does not exist

1 error has been reported␠
`},
		{[]string{"T", "T"}, 0, `Testing T.UC2
Archive is not damage protected
Analyzing␠
Verifying README.TXT OK
Verifying DATA.BIN OK
Verifying NOTE.TXT OK

Everything went OK
`},
		// The order follows the masters of this archive; r2 extracts it so.
		{[]string{"E", "T", "#OUT"}, 0, `Extracting files from T.UC2 (destination path OUT\)
Analyzing␠
Decompressing OUT\NOTE.TXT OK
Decompressing OUT\DATA.BIN OK
Decompressing OUT\README.TXT OK

Everything went OK
`},
		{[]string{"EF", "T", "#OUT"}, 0, `Extracting files from T.UC2 (destination path OUT\)
Analyzing␠
Smart skipping OUT\NOTE.TXT OK
Smart skipping OUT\DATA.BIN OK
Smart skipping OUT\README.TXT OK

Everything went OK
`},
		{[]string{"E", "T", "NOPE.*"}, sevNoMatch, `Extracting files from T.UC2
Analyzing␠
 WARNING 20: no file found matching NOPE.*

1 warning has been reported␠
`},
		{[]string{"M", "T2", "SUB/NOTE.TXT"}, 0, `Adding files to T2.UC2
Scanning␠
Analyzing␠
Compressing SUB\NOTE.TXT DONE
Updated archive contains 1 file (4 bytes)␠␠
Optimizing compression␠

Moving files␠

Everything went OK
`},
		{[]string{"D", "T", "README.TXT"}, 0, `Deleting files from T.UC2
Deleting README.TXT
Updated archive contains 2 files (20,004 bytes)␠␠

Everything went OK
`},
		{[]string{"D", "T", "NOPE.XYZ"}, sevNoMatch, `Deleting files from T.UC2
Updated archive contains 2 files (20,004 bytes)␠␠
 WARNING 20: no file found matching NOPE.XYZ

1 warning has been reported␠
`},
		{[]string{"P", "T"}, 0, `Damage protecting T.UC2
Updated archive contains 2 files (20,004 bytes)␠␠
Damage protecting archive␠

Everything went OK
`},
		{[]string{"T", "T"}, 0, `Testing T.UC2
Testing archive sectors   OK
Testing protection records   OK
Analyzing␠
Verifying DATA.BIN OK
Verifying NOTE.TXT OK

Everything went OK
`},
		{[]string{"U", "T"}, 0, `Removing damage protection from T.UC2
Updated archive contains 2 files (20,004 bytes)␠␠

Everything went OK
`},
	} {
		var out, errs strings.Builder
		if code := run(tc.args, strings.NewReader(""), &out, &errs); code != tc.code {
			t.Fatalf("%q: exit %d, want %d\n%s%s", tc.args, code, tc.code, out.String(), errs.String())
		}
		// ␠ marks the trailing spaces of UC2's lines.
		if got, want := logo(out.String())+errs.String(), strings.ReplaceAll(tc.out, "␠", " "); got != want {
			t.Errorf("%q:\n%s\nwant:\n%s", tc.args, got, want)
		}
	}
	if _, err := os.Stat("SUB/NOTE.TXT"); err == nil {
		t.Error("moved file not deleted")
	}
}

func TestIsTerminalNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("the null device is a terminal")
	}
}
