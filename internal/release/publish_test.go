package release

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var errInjected = errors.New("injected crash")

// fakeForge is an in-memory GitHub. Mutating calls are numbered; with
// failAt = k the k-th mutating call fails, either before its effect
// (crashBefore) or after it (the response is lost).
type fakeForge struct {
	releases   []Release
	nextID     int64
	tags       map[Version]string // created by Publish, like GitHub
	main       []fakeCommit       // main's history, tip last
	mutations  int
	failAt     int
	failBefore bool
	moveOnce   bool // CommitFiles reports errBranchMoved once (someone pushed)
}

type fakeCommit struct {
	sha     string
	message string
	files   map[string]string
}

func newFakeForge(published ...Release) *fakeForge {
	f := &fakeForge{nextID: 100, tags: map[Version]string{}}
	f.releases = append(f.releases, published...)
	for _, r := range published {
		f.tags[r.Version] = "legacy"
	}
	return f
}

func (f *fakeForge) mutate(effect func()) error {
	f.mutations++
	if f.mutations == f.failAt {
		if !f.failBefore {
			effect()
		}
		return errInjected
	}
	effect()
	return nil
}

func (f *fakeForge) find(id int64) *Release {
	for i := range f.releases {
		if f.releases[i].ID == id {
			return &f.releases[i]
		}
	}
	return nil
}

func (f *fakeForge) Releases(context.Context) ([]Release, error) {
	out := make([]Release, len(f.releases))
	for i, r := range f.releases {
		r.Assets = append([]Asset(nil), r.Assets...)
		out[i] = r
	}
	return out, nil
}

func (f *fakeForge) CreateDraft(_ context.Context, t Target) (Release, error) {
	var r Release
	err := f.mutate(func() {
		f.nextID++
		r = Release{ID: f.nextID, Version: t.Version, Draft: true, Commit: t.Commit, Body: t.Notes}
		f.releases = append(f.releases, r)
	})
	return r, err
}

func (f *fakeForge) UpdateDraft(_ context.Context, id int64, t Target) error {
	return f.mutate(func() { f.find(id).Body = t.Notes })
}

func (f *fakeForge) DeleteRelease(_ context.Context, id int64) error {
	return f.mutate(func() {
		for i := range f.releases {
			if f.releases[i].ID == id {
				f.releases = append(f.releases[:i], f.releases[i+1:]...)
				return
			}
		}
	})
}

func (f *fakeForge) UploadAsset(_ context.Context, id int64, a Artifact) (Asset, error) {
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return Asset{}, err
	}
	var up Asset
	err = f.mutate(func() {
		r := f.find(id)
		for _, existing := range r.Assets {
			if existing.Name == a.Name {
				panic("GitHub rejects a duplicate asset name: " + a.Name)
			}
		}
		f.nextID++
		up = Asset{ID: f.nextID, Name: a.Name, Digest: sha256.Sum256(data)}
		r.Assets = append(r.Assets, up)
	})
	return up, err
}

func (f *fakeForge) DeleteAsset(_ context.Context, id int64) error {
	return f.mutate(func() {
		for ri := range f.releases {
			as := f.releases[ri].Assets
			for i := range as {
				if as[i].ID == id {
					f.releases[ri].Assets = append(as[:i], as[i+1:]...)
					return
				}
			}
		}
	})
}

func (f *fakeForge) Publish(_ context.Context, id int64) (Release, error) {
	var r Release
	err := f.mutate(func() {
		p := f.find(id)
		p.Draft, p.PublishedAt = false, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
		f.tags[p.Version] = p.Commit
		r = *p
	})
	return r, err
}

func (f *fakeForge) tip() fakeCommit { return f.main[len(f.main)-1] }

func (f *fakeForge) ReadBranch(_ context.Context, name string) (Branch, error) {
	tip := f.tip()
	br := Branch{Name: name, Commit: tip.sha, Tree: treeOf(tip.files)}
	if s, ok := tip.files[formulaPath]; ok {
		br.Formula = []byte(s)
	}
	br.Changelog = []byte(tip.files[changelogPath])
	return br, nil
}

func (f *fakeForge) Compare(_ context.Context, commit, _ string) (Comparison, error) {
	for i, c := range f.main {
		if c.sha == commit {
			var ahead []Commit
			for _, a := range f.main[i+1:] {
				ahead = append(ahead, Commit{SHA: a.sha, Parents: 1, Message: a.message})
			}
			return Comparison{OnBranch: true, Ahead: ahead}, nil
		}
	}
	return Comparison{}, nil
}

func (f *fakeForge) CommitFiles(_ context.Context, parent Branch, files map[string][]byte, message string) (string, error) {
	if f.moveOnce {
		f.moveOnce = false
		f.pushMain("docs: someone else pushed", nil)
		return "", errBranchMoved
	}
	if parent.Commit != f.tip().sha {
		return "", errBranchMoved
	}
	next := map[string]string{}
	for k, v := range f.tip().files {
		next[k] = v
	}
	for k, v := range files {
		next[k] = string(v)
	}
	if treeOf(next) == parent.Tree {
		return "", errors.New("fake: empty commit")
	}
	var sha string
	err := f.mutate(func() { sha = f.pushMain(message, files) })
	return sha, err
}

func (f *fakeForge) pushMain(message string, files map[string][]byte) string {
	next := map[string]string{}
	if len(f.main) > 0 {
		for k, v := range f.tip().files {
			next[k] = v
		}
	}
	for k, v := range files {
		next[k] = string(v)
	}
	sha := fmt.Sprintf("%040x", len(f.main)+1)
	f.main = append(f.main, fakeCommit{sha: sha, message: message, files: next})
	return sha
}

func treeOf(files map[string]string) string {
	h := sha256.New()
	for _, k := range sortedKeys(files) {
		fmt.Fprintf(h, "%s\x00%s\x00", k, files[k])
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// legacyRelease is v0.5.0 exactly as GitHub reports it.
func legacyRelease() Release {
	d := func(s string) Digest {
		dg, err := parseAssetDigest("sha256:" + s)
		if err != nil {
			panic(err)
		}
		return dg
	}
	return Release{ID: 5, Version: v(0, 5, 0), Commit: "main", Body: "Slice release v0.5.0. See CHANGELOG.md for the scope and acceptance tests.",
		PublishedAt: time.Date(2026, 10, 30, 13, 23, 21, 0, time.UTC),
		Assets: []Asset{
			{ID: 51, Name: "evalkit_v0.5.0_darwin_amd64.tar.gz", Digest: d("dc69ed63cee654ecf95e3bf11a9580b0cc4f93e5a383a83d9910648e8fac5738")},
			{ID: 52, Name: "evalkit_v0.5.0_darwin_arm64.tar.gz", Digest: d("ee447c22a5e9ae084e7682e0528c7bfb95f7f7cb911ea26f9d1f4bcbd88969c4")},
			{ID: 53, Name: "evalkit_v0.5.0_linux_amd64.tar.gz", Digest: d("78350946b491af1d73edead6471bf2f67b9cb037baa741601d6859d0d93f3df8")},
			{ID: 54, Name: "evalkit_v0.5.0_linux_arm64.tar.gz", Digest: d("5fbe0824e7d109181a5465565aaca83f9d857fdaf7d6521ea2bdcfa96dca0280")},
			{ID: 55, Name: "SHA256SUMS", Digest: d("639f5b90c87b424a6f8347249b2317c10ec6edb79749f1735f9800df1b9743a8")},
		}}
}

var testRepo = Repo{"ai-evals", "evalkit"}

const changelog050 = "# Changelog\n\nIntro.\n\n## [0.5.0] - 2026-10-30 — V5: Verified historical inspection\n\n### Added\n- x\n"

// fakeBuild produces deterministic stand-in archives, like a reproducible
// build: same version, same bytes.
func fakeBuild(dir string) func(context.Context, Target) (Bundle, error) {
	return func(_ context.Context, t Target) (Bundle, error) {
		out := filepath.Join(dir, t.Version.Tag())
		if err := os.MkdirAll(out, 0o755); err != nil {
			return Bundle{}, err
		}
		archives := map[Platform]Artifact{}
		for _, p := range platforms {
			a, err := writeArtifact(out, archiveName(t.Version, p), []byte("evalkit "+t.Version.String()+" "+p.OS+"/"+p.Arch))
			if err != nil {
				return Bundle{}, err
			}
			archives[p] = a
		}
		return newBundle(t.Version, archives, out)
	}
}

// world is the starting state: v0.5.0 published, main at head with the
// v0.5.0 formula and changelog.
func world(t *testing.T) (*fakeForge, string) {
	t.Helper()
	f := newFakeForge(legacyRelease())
	formula, err := renderFormula(testRepo, mustSums(t, legacyRelease()))
	if err != nil {
		t.Fatal(err)
	}
	head := f.pushMain("fix(inspect): reject scorer contracts", map[string][]byte{formulaPath: formula, changelogPath: []byte(changelog050)})
	return f, head
}

func mustSums(t *testing.T, r Release) Checksums {
	t.Helper()
	s, err := checksumsOf(r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// planFor is what PlanRelease returns on a fresh checkout of head: the tag
// exists in git once GitHub published the release (checkout fetches tags).
func planFor(f *fakeForge, head string) Plan {
	target := Target{Version: v(0, 5, 1), Bump: PatchBump, Commit: head, Notes: "### Bug Fixes\n\n- **inspect:** reject scorer contracts (ea7f1f1)"}
	if f.tags[target.Version] == head {
		target.Tagged, target.Bump = true, NoBump
	}
	return Plan{Head: head, Target: &target}
}

func assertConverged(t *testing.T, f *fakeForge, head string, startCommits int) {
	t.Helper()
	var published []Release
	for _, r := range f.releases {
		if r.Draft {
			t.Fatalf("draft left behind: %+v", r)
		}
		if r.Version == v(0, 5, 1) {
			published = append(published, r)
		}
	}
	if len(published) != 1 || published[0].Commit != head || len(published[0].Assets) != len(platforms)+1 || f.tags[v(0, 5, 1)] != head {
		t.Fatalf("v0.5.1 not converged: %+v tags=%v", published, f.tags)
	}
	added := f.main[startCommits:]
	if len(added) != 1 || added[0].message != "chore(release): v0.5.1" {
		var msgs []string
		for _, c := range added {
			msgs = append(msgs, c.message)
		}
		t.Fatalf("main commits added = %q, want exactly one chore(release): v0.5.1", msgs)
	}
	for i := 1; i < len(f.main); i++ {
		if treeOf(f.main[i].files) == treeOf(f.main[i-1].files) {
			t.Fatalf("empty commit on main: %q", f.main[i].message)
		}
	}
	tip := f.tip().files
	if !strings.Contains(tip[formulaPath], `version "0.5.1"`) || !strings.Contains(tip[changelogPath], "## [0.5.1] - 2026-11-01\n\n### Bug Fixes") {
		t.Fatalf("main files not synced:\n%s\n%s", tip[formulaPath], tip[changelogPath])
	}
	if strings.Index(tip[changelogPath], "## [0.5.1]") > strings.Index(tip[changelogPath], "## [0.5.0]") {
		t.Fatal("new CHANGELOG section must go above the existing ones")
	}
}

// TestPublishConvergesFromEveryCrashPoint is the recovery contract. For
// every mutating call k and both crash modes: run publish until it fails
// at k, run it again with no faults, and require the fault-free end state.
// A third run must make zero mutating calls.
func TestPublishConvergesFromEveryCrashPoint(t *testing.T) {
	ctx := context.Background()
	f, head := world(t)
	p := publisher{forge: f, repo: testRepo, build: fakeBuild(t.TempDir())}
	if _, err := p.run(ctx, planFor(f, head)); err != nil {
		t.Fatal(err)
	}
	assertConverged(t, f, head, 1)
	total := f.mutations
	if total < 7 {
		t.Fatalf("only %d mutating calls; the crash sweep would be vacuous", total)
	}

	for k := 1; k <= total; k++ {
		for _, before := range []bool{true, false} {
			t.Run(fmt.Sprintf("crash at call %d before=%v", k, before), func(t *testing.T) {
				f, head := world(t)
				p := publisher{forge: f, repo: testRepo, build: fakeBuild(t.TempDir())}
				f.failAt, f.failBefore = k, before
				if _, err := p.run(ctx, planFor(f, head)); !errors.Is(err, errInjected) {
					t.Fatalf("first run err = %v, want the injected crash", err)
				}
				f.failAt = 0
				if _, err := p.run(ctx, planFor(f, head)); err != nil {
					t.Fatalf("recovery run: %v", err)
				}
				assertConverged(t, f, head, 1)
				f.mutations = 0
				rep, err := p.run(ctx, planFor(f, head))
				if err != nil || f.mutations != 0 || rep.Main.Commit != "" {
					t.Fatalf("third run made %d mutating calls (commit %q, err %v)", f.mutations, rep.Main.Commit, err)
				}
			})
		}
	}
}

// After a publish, re-running on the same head while main only has the
// tool's own commit is not stale, and is a no-op.
func TestRerunAfterSyncIsNoop(t *testing.T) {
	ctx := context.Background()
	f, head := world(t)
	p := publisher{forge: f, repo: testRepo, build: fakeBuild(t.TempDir())}
	if _, err := p.run(ctx, planFor(f, head)); err != nil {
		t.Fatal(err)
	}
	f.mutations = 0
	rep, err := p.run(ctx, planFor(f, head))
	if err != nil || rep.Release.Action != AlreadyPublished || f.mutations != 0 {
		t.Fatalf("rep=%+v mutations=%d err=%v", rep.Release, f.mutations, err)
	}
	if !strings.Contains(rep.Markdown(), "already describes v0.5.1, no commit") {
		t.Fatalf("markdown:\n%s", rep.Markdown())
	}
}

// Stale-run guard: a dispatch or re-run on head after main gained a
// releasable commit publishes nothing; the newer run releases cumulatively.
func TestStaleRunPublishesNothing(t *testing.T) {
	ctx := context.Background()
	f, head := world(t)
	f.pushMain("feat(sdk): newer work", nil)
	p := publisher{forge: f, repo: testRepo, build: fakeBuild(t.TempDir())}
	rep, err := p.run(ctx, planFor(f, head))
	if err != nil || rep.Release.Action != SkippedStale || f.mutations != 0 {
		t.Fatalf("rep=%+v mutations=%d err=%v", rep.Release, f.mutations, err)
	}
	for _, r := range f.releases {
		if r.Version == v(0, 5, 1) {
			t.Fatalf("stale run created %+v", r)
		}
	}
}

// main moving between read and commit: recompute on the new tip, never force.
func TestSyncMainRetriesWhenMainMoves(t *testing.T) {
	ctx := context.Background()
	f, head := world(t)
	f.moveOnce = true
	p := publisher{forge: f, repo: testRepo, build: fakeBuild(t.TempDir())}
	rep, err := p.run(ctx, planFor(f, head))
	if err != nil || rep.Main.Commit != f.tip().sha || f.main[1].message != "docs: someone else pushed" {
		t.Fatalf("rep=%+v err=%v", rep.Main, err)
	}
}

// A stale draft from a crashed run on an older head is deleted, and the
// formula never downgrades when an older release is listed later.
func TestStaleDraftsRemoved(t *testing.T) {
	ctx := context.Background()
	f, head := world(t)
	f.releases = append(f.releases, Release{ID: 7, Version: v(0, 5, 1), Draft: true, Commit: "0ld"}, Release{ID: 8, Version: v(0, 4, 9), Draft: true, Commit: "x"})
	p := publisher{forge: f, repo: testRepo, build: fakeBuild(t.TempDir())}
	rep, err := p.run(ctx, planFor(f, head))
	if err != nil || len(rep.Release.Removed) != 2 {
		t.Fatalf("rep=%+v err=%v", rep.Release, err)
	}
	assertConverged(t, f, head, 1)
}

// A published release without its tag in git is never silently retagged.
func TestPublishedWithoutTagRefuses(t *testing.T) {
	ctx := context.Background()
	f, head := world(t)
	f.releases = append(f.releases, Release{ID: 9, Version: v(0, 5, 1), Commit: "elsewhere", PublishedAt: time.Now()})
	plan := planFor(f, head)
	plan.Target.Tagged = false
	p := publisher{forge: f, repo: testRepo, build: fakeBuild(t.TempDir())}
	if _, err := p.run(ctx, plan); err == nil || !strings.Contains(err.Error(), "never retag") {
		t.Fatalf("err = %v", err)
	}
}
