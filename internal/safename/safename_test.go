package safename

import "testing"

func TestDotGit(t *testing.T) {
	zwnj, bom := string(rune(0x200C)), string(rune(0xFEFF))
	for name, want := range map[string]bool{
		".git": true, ".GIT": true, ".git.": true, ".git ": true, ".git::$INDEX_ALLOCATION": true,
		"GIT~1": true, "git~12": true, ".g" + zwnj + "it": true, bom + ".git": true,
		"git": false, ".gitignore": false, "GIT~": false, "GIT~1A": false, "x.git": false,
	} {
		if DotGit(name) != want {
			t.Errorf("DotGit(%q) != %v", name, want)
		}
	}
	if Alias(".git") || !Alias("GIT~1") || !Alias(".Git") {
		t.Fatal("Alias")
	}
}
