package expand

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// pemBlock matches generated certificates and keys. Since Go 1.26 crypto
// ignores a custom random source, so they differ on every run even with
// Rand = zeros{}; the golden files hold a placeholder instead.
var pemBlock = regexp.MustCompile(`-----BEGIN ([A-Z ]+)-----[^-]*-----END ([A-Z ]+)-----`)

// TestGolden expands every lab in examples/ and compares the low-level
// topology with testdata/golden/<name>.yaml, so a refactor of the
// expanders can't change what a lab runs without the diff showing up
// here. PARKER_UPDATE_GOLDEN=1 rewrites the files.
func TestGolden(t *testing.T) {
	Rand = zeros{}
	defer func() { Rand = rand.Reader }()
	labs, err := filepath.Glob("../../examples/*.yaml")
	if err != nil || len(labs) == 0 {
		t.Fatalf("no examples: %v", err)
	}
	update := os.Getenv("PARKER_UPDATE_GOLDEN") == "1"
	for _, lab := range labs {
		name := strings.TrimSuffix(filepath.Base(lab), ".yaml")
		t.Run(name, func(t *testing.T) {
			low, err := Expand(load(t, lab))
			if err != nil {
				t.Fatal(err)
			}
			b, err := yaml.MarshalWithOptions(low, yaml.UseLiteralStyleIfMultiline(true), yaml.IndentSequence(true))
			if err != nil {
				t.Fatal(err)
			}
			got := pemBlock.ReplaceAll(b, []byte("<generated $1>"))
			path := filepath.Join("testdata", "golden", name+".yaml")
			if update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run with PARKER_UPDATE_GOLDEN=1 to create it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("expanded %s differs from %s; if the change is intended, rerun with PARKER_UPDATE_GOLDEN=1 and review the diff\n%s", lab, path, firstDiff(string(want), string(got)))
			}
		})
	}
}

// firstDiff shows the first line that differs, with a little context.
func firstDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return "line " + strconv.Itoa(i+1) + ":\n- " + wl + "\n+ " + gl
		}
	}
	return ""
}
