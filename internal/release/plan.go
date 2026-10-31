package release

import (
	"context"
	"fmt"
	"strings"
)

// Tag is a release tag as git sees it, positioned relative to the planned head.
type Tag struct {
	Version Version
	Commit  string   // peeled commit SHA (annotated tags are dereferenced)
	Rel     Relation // where Commit sits relative to the planned head
}

// Relation of a tag's commit to the planned head.
type Relation uint8

const (
	AtHead     Relation = iota // tag points at head
	Ancestor                   // tag is in head's history (`git tag --merged`)
	Descendant                 // head is in the tag's history (`git tag --contains`): head is older
	Unrelated                  // neither: a tag off main's lineage
)

// Plan is what the planned head should be published as. It is derived only
// from git history and tags, so `plan` locally, `plan` in a PR and `publish`
// on main all compute the same answer for the same commit.
type Plan struct {
	Head    string   // full SHA of the commit being planned
	Base    *Tag     // newest release tag strictly below Target in head's history; nil before the first release
	Changes []Change // Conventional Commits in Base..Head, oldest first
	Ignored []Commit // non-merge commits in Base..Head whose subject is not a Conventional Commit
	Target  *Target  // nil: head needs no new release (Skip says why)
	Skip    Skip
}

// Skip explains a nil Plan.Target.
type Skip uint8

const (
	NotSkipped          Skip = iota
	NoReleasableChanges      // only chore/docs/ci/... since Base
	AlreadyReleased          // a newer release tag already contains head (re-run of an old run)
)

// Target is a release to converge to. Constructed only by planning, so a
// Target always names a CI-tested commit and a version derived from it.
type Target struct {
	Version Version
	Bump    Bump   // NoBump when resuming a tag an earlier attempt created
	Commit  string // == Plan.Head; the tag will point here
	Tagged  bool   // git already has Version's tag at Commit: an earlier attempt published (or a human tagged)
	Notes   string // markdown body rendered from Changes; the GitHub release body and the CHANGELOG section body
}

// PlanRequest names the checkout and the revision to plan.
type PlanRequest struct {
	Dir  string // git checkout with full history and tags (fetch-depth: 0)
	Head string // revision: "HEAD" locally, $GITHUB_SHA in the release job
}

// PlanRelease reads git and returns the plan. Read-only; never touches the
// network. This is the same function Publish calls first.
func PlanRelease(ctx context.Context, req PlanRequest) (Plan, error) {
	g := gitRepo{dir: req.Dir}
	head, err := g.resolve(ctx, req.Head)
	if err != nil {
		return Plan{}, err
	}
	tags, err := g.releaseTags(ctx, head)
	if err != nil {
		return Plan{Head: head}, err
	}
	base, headTag, skip, err := position(tags)
	if err != nil || skip == AlreadyReleased {
		return Plan{Head: head, Skip: skip}, err
	}
	commits, err := g.log(ctx, base, head)
	if err != nil {
		return Plan{Head: head}, err
	}
	modulePath, err := g.modulePath(ctx, head)
	if err != nil {
		return Plan{Head: head}, err
	}
	return decide(head, base, headTag, commits, modulePath)
}

func maxTag(tags []Tag, keep func(Tag) bool) *Tag {
	var best *Tag
	for i := range tags {
		if keep(tags[i]) && (best == nil || best.Version.Less(tags[i].Version)) {
			best = &tags[i]
		}
	}
	return best
}

// position picks the base tag and detects re-runs. Pure.
func position(tags []Tag) (base, headTag *Tag, skip Skip, err error) {
	newest := maxTag(tags, func(Tag) bool { return true })
	if newest == nil {
		return nil, nil, NotSkipped, nil
	}
	switch newest.Rel {
	case Descendant:
		return nil, nil, AlreadyReleased, nil
	case Unrelated:
		return nil, nil, NotSkipped, fmt.Errorf("release: newest tag %s (%s) is not on head's lineage; refusing to guess a base", newest.Version.Tag(), short(newest.Commit))
	}
	headTag = maxTag(tags, func(t Tag) bool { return t.Rel == AtHead })
	base = maxTag(tags, func(t Tag) bool {
		return t.Rel == Ancestor && (headTag == nil || t.Version.Less(headTag.Version))
	})
	return base, headTag, NotSkipped, nil
}

// decide is the versioning policy applied to one range of history. Pure.
func decide(head string, base, headTag *Tag, commits []Commit, modulePath string) (Plan, error) {
	changes, ignored := classify(commits)
	p := Plan{Head: head, Base: base, Changes: changes, Ignored: ignored}
	if headTag != nil {
		p.Target = &Target{Version: headTag.Version, Commit: head, Tagged: true, Notes: renderNotes(changes)}
		return p, nil
	}
	bump := NoBump
	for _, c := range changes {
		bump = max(bump, c.Bump())
	}
	if bump == NoBump {
		p.Skip = NoReleasableChanges
		return p, nil
	}
	from := Version{}
	if base != nil {
		from = base.Version
	}
	next, err := from.Next(bump, modulePath)
	if err != nil {
		return p, err
	}
	p.Target = &Target{Version: next, Bump: bump, Commit: head, Notes: renderNotes(changes)}
	return p, nil
}

// Markdown renders the plan for a terminal or $GITHUB_STEP_SUMMARY:
// head, base, next version and bump, the notes preview, and ignored commits.
func (p Plan) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Release plan for %s\n\n", short(p.Head))
	if p.Base != nil {
		fmt.Fprintf(&b, "- base: %s (%s)\n", p.Base.Version.Tag(), short(p.Base.Commit))
	} else {
		b.WriteString("- base: none (first release)\n")
	}
	switch {
	case p.Target != nil && p.Target.Tagged:
		fmt.Fprintf(&b, "- next: %s (already tagged at head; finishing an earlier attempt)\n", p.Target.Version.Tag())
	case p.Target != nil:
		fmt.Fprintf(&b, "- next: **%s** (%s)\n", p.Target.Version.Tag(), p.Target.Bump)
	case p.Skip == AlreadyReleased:
		b.WriteString("- next: nothing — a newer release already contains this commit\n")
	default:
		b.WriteString("- next: nothing — no feat/fix/perf/revert or breaking commits since base\n")
	}
	notReleased := map[string]int{}
	for _, c := range p.Changes {
		if c.Bump() == NoBump {
			notReleased[c.Type]++
		}
	}
	if len(notReleased) > 0 {
		var parts []string
		for _, t := range sortedKeys(notReleased) {
			parts = append(parts, fmt.Sprintf("%d %s", notReleased[t], t))
		}
		fmt.Fprintf(&b, "- not released: %s\n", strings.Join(parts, ", "))
	}
	if len(p.Ignored) > 0 {
		fmt.Fprintf(&b, "- non-conventional (ignored): %d\n", len(p.Ignored))
		for _, c := range p.Ignored {
			fmt.Fprintf(&b, "  - %s %s\n", short(c.SHA), c.subject())
		}
	}
	if p.Target != nil && p.Target.Notes != "" {
		b.WriteString("\n" + p.Target.Notes)
	}
	return b.String()
}
