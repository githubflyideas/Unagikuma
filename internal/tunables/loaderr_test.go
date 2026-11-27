package tunables

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestLoadError(t *testing.T) {
	if err := LoadError(); err != nil {
		t.Fatal(err)
	}
}

// TestKeysUsedAreDefined keeps every key the program reads in the table:
// an unknown key reads as unset, so a typo would silently use zero.
func TestKeysUsedAreDefined(t *testing.T) {
	re := regexp.MustCompile(`\.(Int|Float|Duration|Bool|TimeOfDay|TimesOfDay|WeekTime|Rate|Raw)\("([a-z0-9.-]+)"\)`)
	root := filepath.Join("..", "..")
	n := 0
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if !strings.Contains(m[2], ".") {
				continue // not a tunable key
			}
			n++
			if _, ok := index[m[2]]; !ok {
				t.Errorf("%s reads unknown tunable %q", p, m[2])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no tunable reads found: the pattern is out of date")
	}
}
