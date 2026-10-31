package release

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PublishRequest is everything `release publish` needs.
type PublishRequest struct {
	Dir    string // checkout of Head, full history and tags
	Head   string // $GITHUB_SHA: the commit the test job just passed
	GitHub GitHub
	OutDir string // scratch dir for archives (the cmd passes $RUNNER_TEMP)
}

// Report is what one publish run did, for the step summary. It is returned
// alongside an error too, so a failed run still says how far it got.
type Report struct {
	Plan    Plan
	Release ReleaseOutcome
	Main    MainOutcome
}

// ReleaseOutcome says what happened to Plan.Target on GitHub.
type ReleaseOutcome struct {
	Action   ReleaseAction
	Uploaded []string // asset names uploaded this run
	Removed  []string // stale drafts or stale assets deleted this run
}

type ReleaseAction uint8

const (
	NoTarget         ReleaseAction = iota // plan had nothing to publish
	AlreadyPublished                      // an earlier attempt finished; nothing touched
	PublishedNow                          // draft converged and published this run
	SkippedStale                          // main has newer releasable commits; that run publishes
)

// MainOutcome says whether main needed the derived files rewritten.
type MainOutcome struct {
	For     Version // newest published release the files now describe
	Commit  string  // "" when main already matched: no commit was made
	Changed []string
}

// releaseCommitPrefix marks the tool's own commits on main. They carry no
// releasable change and are ignored by the stale-run guard.
const releaseCommitPrefix = "chore(release): "

const mainBranch = "main"

// Publish converges GitHub and main to the plan for req.Head. Every step
// reads current state and applies only the difference, so a re-run of the
// same job, at any point a previous attempt stopped, finishes the work and
// never creates an empty commit:
//
//  1. plan     PlanRelease(req.Dir, req.Head)                      (git only)
//  2. guard    skip the release if main moved past Head with non-tool commits
//  3. release  if plan.Target != nil: ensurePublished               (draft -> assets -> publish)
//  4. main     syncMain: Formula + CHANGELOG for the published releases, one commit or none
//
// Step 4 runs even when step 3 had nothing to do, so a sync that failed
// after a publish is repaired by re-running the job, by workflow_dispatch,
// or by the next push to main.
func Publish(ctx context.Context, req PublishRequest) (Report, error) {
	var rep Report
	plan, err := PlanRelease(ctx, PlanRequest{Dir: req.Dir, Head: req.Head})
	rep.Plan = plan
	if err != nil {
		return rep, err
	}
	g := gitRepo{dir: req.Dir}
	p := publisher{forge: newRESTForge(req.GitHub), repo: req.GitHub.Repo, build: func(ctx context.Context, t Target) (Bundle, error) {
		co, err := g.verifyCheckout(ctx, t.Commit)
		if err != nil {
			return Bundle{}, err
		}
		return buildBundle(ctx, co, t.Version, req.OutDir)
	}}
	return p.run(ctx, plan)
}

// publisher holds the effectful dependencies so tests substitute both.
type publisher struct {
	forge forge
	repo  Repo
	build func(context.Context, Target) (Bundle, error)
}

// run is steps 2-4 of Publish for an already computed plan.
func (p publisher) run(ctx context.Context, plan Plan) (Report, error) {
	rep := Report{Plan: plan}
	if plan.Target != nil {
		stale, err := p.stale(ctx, plan.Head)
		if err != nil {
			return rep, err
		}
		if stale {
			rep.Release.Action = SkippedStale
		} else if rep.Release, err = p.ensurePublished(ctx, *plan.Target); err != nil {
			return rep, err
		}
	}
	var err error
	rep.Main, err = p.syncMain(ctx)
	return rep, err
}

// stale reports whether a newer run owns the release: main is not at head,
// and main's commits beyond head include something other than the tool's
// own commits (a re-run or dispatch on an old SHA). Head off main is stale too.
func (p publisher) stale(ctx context.Context, head string) (bool, error) {
	c, err := p.forge.Compare(ctx, head, mainBranch)
	if err != nil {
		return false, err
	}
	if !c.OnBranch {
		return true, nil
	}
	for _, commit := range c.Ahead {
		if !strings.HasPrefix(commit.subject(), releaseCommitPrefix) {
			return true, nil
		}
	}
	return false, nil
}

// ensurePublished converges the release for t. Draft-first: nothing is
// public, and no tag exists, until every asset is attached and verified.
func (p publisher) ensurePublished(ctx context.Context, t Target) (ReleaseOutcome, error) {
	var out ReleaseOutcome
	all, err := p.forge.Releases(ctx)
	if err != nil {
		return out, err
	}
	var draft *Release
	for i := range all {
		r := all[i]
		if r.Version != t.Version {
			continue
		}
		if !r.Draft {
			if t.Tagged {
				out.Action = AlreadyPublished
				return out, nil
			}
			return out, fmt.Errorf("release: %s is published but git has no %s tag at %s; investigate, never retag", t.Version.Tag(), t.Version.Tag(), short(t.Commit))
		}
		if draft == nil && r.Commit == t.Commit {
			draft = &all[i]
		}
	}
	if t.Tagged {
		return out, fmt.Errorf("release: tag %s exists at %s but no published release does; publish it manually or delete the tag", t.Version.Tag(), short(t.Commit))
	}
	// Every other semver draft is stale: unpublished and not the current
	// plan, so no run owns it (runs are serialized by the concurrency group).
	for _, r := range all {
		if r.Draft && (draft == nil || r.ID != draft.ID) {
			if err := p.forge.DeleteRelease(ctx, r.ID); err != nil {
				return out, err
			}
			out.Removed = append(out.Removed, "draft "+r.Version.Tag())
		}
	}
	var rel Release
	if draft == nil {
		if rel, err = p.forge.CreateDraft(ctx, t); err != nil {
			return out, err
		}
	} else {
		rel = *draft
		if err := p.forge.UpdateDraft(ctx, rel.ID, t); err != nil {
			return out, err
		}
	}
	b, err := p.build(ctx, t)
	if err != nil {
		return out, err
	}
	want := map[string]Artifact{}
	for _, a := range b.Assets() {
		want[a.Name] = a
	}
	have := map[string]Asset{}
	for _, a := range rel.Assets {
		if _, ok := want[a.Name]; !ok {
			if err := p.forge.DeleteAsset(ctx, a.ID); err != nil {
				return out, err
			}
			out.Removed = append(out.Removed, a.Name)
			continue
		}
		have[a.Name] = a
	}
	for _, a := range b.Assets() {
		if old, ok := have[a.Name]; ok {
			if old.Digest == a.Digest {
				continue
			}
			if err := p.forge.DeleteAsset(ctx, old.ID); err != nil {
				return out, err
			}
		}
		up, err := p.forge.UploadAsset(ctx, rel.ID, a)
		if err != nil {
			return out, err
		}
		if up.Digest != (Digest{}) && up.Digest != a.Digest {
			return out, fmt.Errorf("release: GitHub digest for %s is %s, want %s", a.Name, up.Digest, a.Digest)
		}
		out.Uploaded = append(out.Uploaded, a.Name)
	}
	if err := p.verifyAssets(ctx, rel.ID, want); err != nil {
		return out, err
	}
	if _, err := p.forge.Publish(ctx, rel.ID); err != nil {
		return out, err
	}
	out.Action = PublishedNow
	return out, nil
}

// verifyAssets re-reads the draft and requires exactly the wanted assets,
// each with GitHub's digest equal to the local one.
func (p publisher) verifyAssets(ctx context.Context, id int64, want map[string]Artifact) error {
	all, err := p.forge.Releases(ctx)
	if err != nil {
		return err
	}
	for _, r := range all {
		if r.ID != id {
			continue
		}
		if len(r.Assets) != len(want) {
			return fmt.Errorf("release: draft %s has %d assets, want %d", r.Version.Tag(), len(r.Assets), len(want))
		}
		for _, a := range r.Assets {
			w, ok := want[a.Name]
			if !ok || a.Digest != w.Digest {
				return fmt.Errorf("release: draft asset %s does not match the local build (digest %s)", a.Name, a.Digest)
			}
		}
		return nil
	}
	return fmt.Errorf("release: draft %d disappeared before publish", id)
}

// syncMain makes main's derived files describe the published releases.
func (p publisher) syncMain(ctx context.Context) (MainOutcome, error) {
	all, err := p.forge.Releases(ctx)
	if err != nil {
		return MainOutcome{}, err
	}
	var published []Release
	for _, r := range all {
		if !r.Draft {
			published = append(published, r)
		}
	}
	if len(published) == 0 {
		return MainOutcome{}, nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		br, err := p.forge.ReadBranch(ctx, mainBranch)
		if err != nil {
			return MainOutcome{}, err
		}
		files, newest, err := mainFiles(br, published, p.repo)
		if err != nil {
			return MainOutcome{}, err
		}
		if len(files) == 0 {
			return MainOutcome{For: newest}, nil // the no-empty-commit invariant
		}
		sha, err := p.forge.CommitFiles(ctx, br, files, releaseCommitPrefix+newest.Tag())
		if errors.Is(err, errBranchMoved) {
			continue // someone pushed; recompute on the new tip
		}
		if err != nil {
			return MainOutcome{}, err
		}
		return MainOutcome{For: newest, Commit: sha, Changed: sortedKeys(files)}, nil
	}
	return MainOutcome{}, fmt.Errorf("release: %s kept moving; re-run to sync", mainBranch)
}

// mainFiles is the desired content of the files this tool owns on main,
// returning only entries that differ from br. Pure.
func mainFiles(br Branch, published []Release, repo Repo) (map[string][]byte, Version, error) {
	var newest *Release
	for i := range published {
		if !published[i].Draft && (newest == nil || newest.Version.Less(published[i].Version)) {
			newest = &published[i]
		}
	}
	if newest == nil {
		return nil, Version{}, nil
	}
	sums, err := checksumsOf(*newest)
	if err != nil {
		return nil, Version{}, err
	}
	formula, err := renderFormula(repo, sums)
	if err != nil {
		return nil, Version{}, err
	}
	files := map[string][]byte{}
	if string(formula) != string(br.Formula) {
		files[formulaPath] = formula
	}
	if changelog, changed := changelogFile(br.Changelog, published); changed {
		files[changelogPath] = changelog
	}
	return files, newest.Version, nil
}

// Markdown renders the report for $GITHUB_STEP_SUMMARY: the plan, then one
// line per step saying what changed or that nothing needed to.
func (r Report) Markdown() string {
	var b strings.Builder
	b.WriteString(r.Plan.Markdown())
	b.WriteString("\n## Publish\n\n")
	switch r.Release.Action {
	case NoTarget:
		b.WriteString("- release: nothing to publish\n")
	case AlreadyPublished:
		fmt.Fprintf(&b, "- release: %s already published\n", r.Plan.Target.Version.Tag())
	case PublishedNow:
		fmt.Fprintf(&b, "- release: published %s at %s (uploaded %d assets)\n", r.Plan.Target.Version.Tag(), short(r.Plan.Head), len(r.Release.Uploaded))
	case SkippedStale:
		b.WriteString("- release: skipped — main has newer commits; the run for main's tip publishes them\n")
	}
	if len(r.Release.Removed) > 0 {
		fmt.Fprintf(&b, "- removed: %s\n", strings.Join(r.Release.Removed, ", "))
	}
	switch {
	case r.Main.Commit != "":
		fmt.Fprintf(&b, "- main: committed %s %s%s (%s)\n", short(r.Main.Commit), releaseCommitPrefix, r.Main.For.Tag(), strings.Join(r.Main.Changed, ", "))
	case r.Main.For != (Version{}):
		fmt.Fprintf(&b, "- main: already describes %s, no commit\n", r.Main.For.Tag())
	}
	return b.String()
}

// Build cross-builds the release archives for the plan's commit into outDir
// without publishing anything: the PR-time dry run of the release path. It
// uses the planned version, or the base version when nothing would release.
func Build(ctx context.Context, dir string, plan Plan, outDir string) (Bundle, error) {
	v := Version{}
	switch {
	case plan.Target != nil:
		v = plan.Target.Version
	case plan.Base != nil:
		v = plan.Base.Version
	}
	co, err := gitRepo{dir: dir}.verifyCheckout(ctx, plan.Head)
	if err != nil {
		return Bundle{}, err
	}
	return buildBundle(ctx, co, v, outDir)
}
