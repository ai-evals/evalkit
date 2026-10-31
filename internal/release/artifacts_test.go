package release

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var stanza = regexp.MustCompile(`(?m)^\s*(url|sha256) "([^"]+)"$`)

// TestFormulaGolden renders v0.5.0 from its real published digests and
// compares with testdata/evalkit-0.5.0.rb; checks every platform appears
// as a url; and checks the url/sha256 stanzas equal the formula users
// installed v0.5.0 from (rendered by the deleted scripts/render_formula.py).
func TestFormulaGolden(t *testing.T) {
	got, err := renderFormula(testRepo, mustSums(t, legacyRelease()))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "evalkit-0.5.0.rb")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("formula differs from %s (UPDATE_GOLDEN=1 to accept):\n%s", golden, got)
	}
	for _, p := range platforms {
		if !strings.Contains(string(got), "/releases/download/v0.5.0/"+archiveName(v(0, 5, 0), p)+`"`) {
			t.Errorf("formula has no url for %s/%s", p.OS, p.Arch)
		}
	}
	pythonRendered, err := os.ReadFile(filepath.Join("testdata", "evalkit-0.5.0.python.rb"))
	if err != nil {
		t.Fatal(err)
	}
	if a, b := stanza.FindAllString(string(got), -1), stanza.FindAllString(string(pythonRendered), -1); len(a) != 8 || strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("url/sha256 stanzas differ from the v0.5.0 formula:\n%v\n%v", a, b)
	}
	// The committed tap formula is exactly what the tool renders, so the first
	// sync after this change does not rewrite it.
	committed, err := os.ReadFile(filepath.Join("..", "..", "Formula", "evalkit.rb"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, got) {
		t.Fatal("Formula/evalkit.rb is not the rendering of the newest release; copy testdata/evalkit-0.5.0.rb")
	}
}

func TestChecksumsRequireEveryPlatform(t *testing.T) {
	r := legacyRelease()
	r.Assets = r.Assets[1:]
	if _, err := checksumsOf(r); err == nil {
		t.Fatal("release missing an archive accepted")
	}
}

func TestChangelogFile(t *testing.T) {
	r := Release{Version: v(0, 5, 1), Body: "### Bug Fixes\n\n- fix (abc1234)", PublishedAt: time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC)}
	next, changed := changelogFile([]byte(changelog050), []Release{legacyRelease(), r})
	want := "# Changelog\n\nIntro.\n\n## [0.5.1] - 2026-11-01\n\n### Bug Fixes\n\n- fix (abc1234)\n\n## [0.5.0]"
	if !changed || !strings.HasPrefix(string(next), want) {
		t.Fatalf("changelog:\n%s", next)
	}
	if again, changed := changelogFile(next, []Release{legacyRelease(), r}); changed || !bytes.Equal(again, next) {
		t.Fatal("changelogFile is not convergent")
	}
	// Hand-written sections are never rewritten: a heading already present wins.
	hand := []byte("# Changelog\n\n## [0.5.1] - 2026-10-31 — hand written\n\n- prose\n")
	if _, changed := changelogFile(hand, []Release{r}); changed {
		t.Fatal("hand-written 0.5.1 section overwritten")
	}
}

func TestRenderNotes(t *testing.T) {
	changes, _ := classify([]Commit{
		commit("1111111aaa", "feat(sdk)!: typed runner\n\nBREAKING CHANGE: Runner.Run returns Outcome"),
		commit("2222222bbb", "fix(inspect): reject scorer contracts"),
		commit("3333333ccc", "perf: faster"),
		commit("4444444ddd", "chore: tidy"),
	})
	want := "### ⚠ BREAKING CHANGES\n\n- **sdk:** Runner.Run returns Outcome (1111111)\n\n" +
		"### Features\n\n- **sdk:** typed runner (1111111)\n\n" +
		"### Bug Fixes\n\n- **inspect:** reject scorer contracts (2222222)\n\n" +
		"### Performance Improvements\n\n- faster (3333333)\n"
	if got := renderNotes(changes); got != want {
		t.Fatalf("notes:\n%q\nwant\n%q", got, want)
	}
}

// TestBuildBundleIsReproducible cross-builds every platform twice from this
// checkout and requires identical archive bytes, so a re-run's digests match
// an earlier attempt's uploads.
func TestBuildBundleIsReproducible(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-builds four binaries twice")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	co := checkout{dir: root, commit: "test", commitTime: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
	ctx := context.Background()
	a, err := buildBundle(ctx, co, v(0, 5, 1), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildBundle(ctx, co, v(0, 5, 1), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Assets()) != len(platforms)+1 {
		t.Fatalf("assets = %d", len(a.Assets()))
	}
	for i, x := range a.Assets() {
		if y := b.Assets()[i]; x.Name != y.Name || x.Digest != y.Digest {
			t.Fatalf("%s not reproducible: %s vs %s", x.Name, x.Digest, y.Digest)
		}
	}
	sums, err := os.ReadFile(a.Sums.Path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(sums)), "\n"); len(lines) != len(platforms) || !strings.HasSuffix(lines[0], "  evalkit_v0.5.1_darwin_amd64.tar.gz") {
		t.Fatalf("SHA256SUMS:\n%s", sums)
	}
}
