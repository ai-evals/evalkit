package release

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/ai-evals/evalkit"

func commit(sha, msg string) Commit { return Commit{SHA: sha, Parents: 1, Message: msg} }

// evalkitRange is the real v0.5.0..main range (98e4629) at the time this
// tool was written: one fix, homebrew/verify chores, two merge commits.
var evalkitRange = []Commit{
	commit("8fcb0258370f04d542f1831dbd5d3c833805d67c", "chore(homebrew): update formula for v0.5.0"),
	commit("9cd653f5089c87051881553f2711ac845902f007", "chore(verify): add baseline verification skills and formal verdict check"),
	{SHA: "6318901000000000000000000000000000000000", Parents: 2, Message: "Merge pull request #6 from ai-evals/setup"},
	commit("ea7f1f1000000000000000000000000000000000", "fix(inspect): reject scorer contracts evalkit would never run\n\nThe inspector certified artifacts..."),
	commit("9af406e000000000000000000000000000000000", "chore(homebrew): update formula for v0.5.0"),
	{SHA: "185840f000000000000000000000000000000000", Parents: 2, Message: "Merge pull request #7 from ai-evals/fix/inspector-scorer-contract"},
	commit("98e4629000000000000000000000000000000000", "chore(homebrew): update formula for v0.5.0"),
}

func v(maj, min, pat uint64) Version { return Version{maj, min, pat} }

func with(extra ...Commit) []Commit { return append(append([]Commit{}, evalkitRange...), extra...) }

// TestPositionAndDecide is the versioning policy as a table over real
// evalkit history shapes.
func TestPositionAndDecide(t *testing.T) {
	const head = "98e4629000000000000000000000000000000000"
	v050 := Tag{Version: v(0, 5, 0), Commit: "dd0e7d3", Rel: Ancestor}
	cases := []struct {
		name       string
		tags       []Tag
		commits    []Commit
		modulePath string
		want       *Version // nil: no target
		wantBump   Bump
		wantSkip   Skip
		wantTagged bool
		wantErr    error
		wantAnyErr bool
		ignored    int
	}{
		{name: "real main: fix(inspect) -> patch", tags: []Tag{v050}, commits: evalkitRange, want: &Version{0, 5, 1}, wantBump: PatchBump},
		{name: "feat -> minor", tags: []Tag{v050}, commits: with(commit("a1", "feat(sdk): x")), want: &Version{0, 6, 0}, wantBump: MinorBump},
		{name: "bang at 0.x -> minor, not 1.0.0", tags: []Tag{v050}, commits: with(commit("a1", "feat(sdk)!: x")), want: &Version{0, 6, 0}, wantBump: MajorBump},
		{name: "BREAKING CHANGE footer at 0.x -> minor", tags: []Tag{v050}, commits: with(commit("a1", "fix: x\n\nBREAKING CHANGE: config v1 removed")), want: &Version{0, 6, 0}, wantBump: MajorBump},
		{name: "perf -> patch", tags: []Tag{v050}, commits: []Commit{commit("a1", "perf(engine): faster")}, want: &Version{0, 5, 1}, wantBump: PatchBump},
		{name: "GitHub revert subject -> patch", tags: []Tag{v050}, commits: []Commit{commit("a1", `Revert "feat(sdk): x"`)}, want: &Version{0, 5, 1}, wantBump: PatchBump},
		{name: "breaking at 1.x needs /v2 module path", tags: []Tag{{Version: v(1, 4, 2), Commit: "c1", Rel: Ancestor}}, commits: []Commit{commit("a1", "feat!: drop v1 configs")}, wantErr: ErrNeedsModulePath},
		{name: "breaking at 1.x with /v2 module path -> 2.0.0", tags: []Tag{{Version: v(1, 4, 2), Commit: "c1", Rel: Ancestor}}, commits: []Commit{commit("a1", "feat!: drop v1 configs")}, modulePath: modulePath + "/v2", want: &Version{2, 0, 0}, wantBump: MajorBump},
		{name: "only chore/docs/ci/test -> nothing", tags: []Tag{v050}, commits: []Commit{commit("a1", "chore(release): v0.5.0"), commit("a2", "docs: x"), commit("a3", "ci: x"), commit("a4", "test: x")}, wantSkip: NoReleasableChanges},
		{name: "tag at head: resume an earlier attempt", tags: []Tag{v050, {Version: v(0, 5, 1), Commit: head, Rel: AtHead}}, commits: evalkitRange, want: &Version{0, 5, 1}, wantTagged: true},
		{name: "newer tag contains head: old run", tags: []Tag{v050, {Version: v(0, 6, 0), Commit: "e1", Rel: Descendant}}, commits: evalkitRange, wantSkip: AlreadyReleased},
		{name: "newest tag on another lineage: refuse", tags: []Tag{v050, {Version: v(0, 9, 0), Commit: "f1", Rel: Unrelated}}, commits: evalkitRange, wantAnyErr: true},
		{name: "merge commits dropped, wip ignored", tags: []Tag{v050}, commits: with(commit("a1", "wip")), want: &Version{0, 5, 1}, wantBump: PatchBump, ignored: 1},
		{name: "no tags: first feat is 0.1.0", commits: []Commit{commit("a1", "feat: first")}, want: &Version{0, 1, 0}, wantBump: MinorBump},
		{name: "uppercase type still conventional", tags: []Tag{v050}, commits: []Commit{commit("a1", "Fix(engine): x")}, want: &Version{0, 5, 1}, wantBump: PatchBump},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mp := tc.modulePath
			if mp == "" {
				mp = modulePath
			}
			base, headTag, skip, err := position(tc.tags)
			var p Plan
			if err == nil && skip != AlreadyReleased {
				p, err = decide(head, base, headTag, tc.commits, mp)
			} else {
				p.Skip = skip
			}
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			case tc.wantAnyErr:
				if err == nil {
					t.Fatal("want an error")
				}
				return
			case err != nil:
				t.Fatal(err)
			}
			if tc.want == nil {
				if p.Target != nil || p.Skip != tc.wantSkip {
					t.Fatalf("target=%+v skip=%v, want no target, skip %v", p.Target, p.Skip, tc.wantSkip)
				}
				return
			}
			if p.Target == nil || p.Target.Version != *tc.want || p.Target.Bump != tc.wantBump || p.Target.Tagged != tc.wantTagged || p.Target.Commit != head {
				t.Fatalf("target = %+v, want %v bump %v tagged %v", p.Target, *tc.want, tc.wantBump, tc.wantTagged)
			}
			if len(p.Ignored) != tc.ignored {
				t.Fatalf("ignored = %d, want %d", len(p.Ignored), tc.ignored)
			}
			for _, c := range p.Changes {
				if strings.HasPrefix(c.Description, "Merge pull request") {
					t.Fatalf("merge commit counted: %+v", c)
				}
			}
		})
	}
}

func TestCheckTitle(t *testing.T) {
	for _, ok := range []string{"fix(inspect): reject x", "feat!: y", "ci: z", "chore(release): v0.5.1"} {
		if err := CheckTitle(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"Fix the thing", "feat:no space", "feat(): x", "feat(a b): x", ""} {
		if CheckTitle(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseTag(t *testing.T) {
	if got, ok := ParseTag("v0.5.1"); !ok || got != v(0, 5, 1) || got.Tag() != "v0.5.1" || got.String() != "0.5.1" {
		t.Fatalf("ParseTag(v0.5.1) = %v %v", got, ok)
	}
	for _, bad := range []string{"0.5.1", "v0.05.1", "v1.2", "v1.2.3-rc.1", "latest"} {
		if _, ok := ParseTag(bad); ok {
			t.Errorf("%q parsed as a release tag", bad)
		}
	}
}

// gitFixture builds a real repository with evalkit's history shape so the
// git adapter (tags, ancestry, log parsing, merges) is exercised end to end.
type gitFixture struct {
	t   *testing.T
	dir string
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	f := &gitFixture{t: t, dir: t.TempDir()}
	f.git("init", "-q", "-b", "main")
	f.git("config", "user.email", "t@example.com")
	f.git("config", "user.name", "t")
	f.git("config", "commit.gpgsign", "false")
	f.git("config", "tag.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(f.dir, "go.mod"), []byte("module "+modulePath+"\n\ngo 1.26.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.git("add", "go.mod")
	f.git("commit", "-q", "-m", "feat(v1): walking skeleton")
	return f
}

func (f *gitFixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *gitFixture) commit(msg string) string {
	f.git("commit", "-q", "--allow-empty", "-m", msg)
	return f.git("rev-parse", "HEAD")
}

func TestPlanReleaseOverRealGit(t *testing.T) {
	ctx := context.Background()
	f := newGitFixture(t)
	f.git("tag", "v0.1.0")
	f.commit("feat(v5): inspection")
	f.git("tag", "-a", "v0.5.0", "-m", "annotated") // annotated tags are peeled
	f.commit("chore(homebrew): update formula for v0.5.0")
	f.git("checkout", "-q", "-b", "fix")
	f.commit("fix(inspect): reject scorer contracts")
	f.git("checkout", "-q", "main")
	f.commit("chore(homebrew): update formula for v0.5.0")
	f.git("merge", "-q", "--no-ff", "fix", "-m", "Merge pull request #7 from ai-evals/fix")
	head := f.commit("wip")

	p, err := PlanRelease(ctx, PlanRequest{Dir: f.dir, Head: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Head != head || p.Base == nil || p.Base.Version != v(0, 5, 0) || p.Target == nil || p.Target.Version != v(0, 5, 1) {
		t.Fatalf("plan = %+v target %+v", p, p.Target)
	}
	if len(p.Ignored) != 1 || len(p.Changes) != 3 {
		t.Fatalf("changes=%d ignored=%d, want 3 and 1 (merge dropped)", len(p.Changes), len(p.Ignored))
	}
	if !strings.Contains(p.Markdown(), "**v0.5.1** (patch)") || !strings.Contains(p.Target.Notes, "**inspect:** reject scorer contracts") {
		t.Fatalf("markdown:\n%s", p.Markdown())
	}

	// Re-run after publish: GitHub created the tag at head.
	f.git("tag", "v0.5.1", head)
	p, err = PlanRelease(ctx, PlanRequest{Dir: f.dir, Head: head})
	if err != nil || p.Target == nil || !p.Target.Tagged || p.Target.Version != v(0, 5, 1) || p.Base.Version != v(0, 5, 0) {
		t.Fatalf("resume plan = %+v, %v", p.Target, err)
	}

	// An old run after a newer release: nothing to do.
	f.commit("feat(sdk): more")
	f.git("tag", "v0.6.0")
	old, err := PlanRelease(ctx, PlanRequest{Dir: f.dir, Head: head + "~1"})
	if err != nil || old.Target != nil || old.Skip != AlreadyReleased {
		t.Fatalf("old run plan = %+v, %v", old, err)
	}
}
